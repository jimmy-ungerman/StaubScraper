package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	userAgent = "staubscrape (+https://github.com/jimmy-ungerman/staubScrape)"

	pollInterval    = 2 * time.Hour
	scrapeFreshness = pollInterval + 15*time.Minute
)

var (
	baseURL   = "https://edstaub.myfuelportal.com"
	email     = os.Getenv("EMAIL")
	password  = os.Getenv("PASSWORD")
	gallonsRe = regexp.MustCompile(`Approximately\s+([\d,]+)\s+gallons`)
)

type Tank struct {
	TankName string `json:"tank_name"`
	Gallons  int    `json:"gallons"`
	Percent  int    `json:"percent"`
}

var cache struct {
	sync.RWMutex
	data      []Tank
	err       string
	updatedAt time.Time
}

var errLoginRequired = errors.New("not logged in; check EMAIL and PASSWORD")

func newClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}, nil
}

// fetch requests a page and parses it. A page that still shows the login form
// means the session was never established or has expired.
func fetch(client *http.Client, req *http.Request) (*goquery.Document, error) {
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", req.URL.Path, err)
	}
	return doc, nil
}

func isLoginPage(doc *goquery.Document) bool {
	return doc.Find(`form[action="/Account/Login"]`).Length() > 0
}

func login(client *http.Client) error {
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/Account/Login", nil)
	doc, err := fetch(client, req)
	if err != nil {
		return err
	}
	token, ok := doc.Find(`form[action="/Account/Login"] input[name="__RequestVerificationToken"]`).Attr("value")
	if !ok {
		return fmt.Errorf("login form has no verification token")
	}

	form := url.Values{
		"__RequestVerificationToken": {token},
		"EmailAddress":               {email},
		"Password":                   {password},
		"RememberMe":                 {"false"},
	}
	req, _ = http.NewRequest(http.MethodPost, baseURL+"/Account/Login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	doc, err = fetch(client, req)
	if err != nil {
		return err
	}
	if isLoginPage(doc) {
		return errLoginRequired
	}
	return nil
}

func scrape() ([]Tank, error) {
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	log.Print("step: login")
	if err := login(client); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	log.Print("step: fetch tanks")
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/Tank", nil)
	doc, err := fetch(client, req)
	if err != nil {
		return nil, err
	}
	if isLoginPage(doc) {
		return nil, errLoginRequired
	}
	return parseTanks(doc)
}

// parseTanks reads every tank row that reports a level. Rows without a gauge
// (e.g. counter-sale accounts) are skipped.
func parseTanks(doc *goquery.Document) ([]Tank, error) {
	var tanks []Tank
	var rowErr error
	doc.Find("div.tank-row").EachWithBreak(func(_ int, row *goquery.Selection) bool {
		bar := row.Find("div.progress-bar")
		if bar.Length() == 0 {
			return true
		}
		percentText, _ := bar.Attr("aria-valuenow")
		tank, err := tankFromExtract(
			row.Find("span.text-larger").First().Text(),
			percentText,
			bar.Parent().Parent().Text(),
		)
		if err != nil {
			rowErr = err
			return false
		}
		tanks = append(tanks, tank)
		return true
	})
	if rowErr != nil {
		return nil, rowErr
	}
	if len(tanks) == 0 {
		return nil, fmt.Errorf("no tank readings found")
	}
	return tanks, nil
}

func tankFromExtract(tankName, percentText, gallonsText string) (Tank, error) {
	name := strings.TrimSpace(tankName)
	if name == "" {
		return Tank{}, fmt.Errorf("tank name missing")
	}
	m := gallonsRe.FindStringSubmatch(gallonsText)
	if len(m) < 2 {
		return Tank{}, fmt.Errorf("gallons text missing")
	}
	gallons, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	if err != nil {
		return Tank{}, fmt.Errorf("parse gallons: %w", err)
	}
	pct, _ := strconv.ParseFloat(percentText, 64)
	return Tank{
		TankName: name,
		Gallons:  gallons,
		Percent:  int(math.Round(pct)),
	}, nil
}

func poll() {
	ticker := time.NewTicker(pollInterval)
	for {
		tanks, err := scrape()
		cache.Lock()
		if err != nil {
			cache.err = err.Error()
			log.Printf("scrape failed: %v", err)
		} else {
			cache.data = tanks
			cache.err = ""
			cache.updatedAt = time.Now().UTC()
			log.Printf("cache updated: %+v", tanks)
		}
		cache.Unlock()
		<-ticker.C
	}
}

func tanksHandler(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	cache.RLock()
	defer cache.RUnlock()

	// No successful scrape yet (startup)
	if cache.data == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "data not yet available, please retry shortly"})
		return
	}
	// Scrape failed but we have stale data — serve it so HA never sees a gap
	json.NewEncoder(w).Encode(map[string]any{
		"tanks":      cache.data,
		"updated_at": cache.updatedAt,
		"stale":      cache.err != "",
	})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func scrapeHealthHandler(w http.ResponseWriter, r *http.Request) {
	if !authorize(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	cache.RLock()
	defer cache.RUnlock()

	if errMsg := scrapeUnhealthyReason(); errMsg != "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"status":     "fail",
			"error":      errMsg,
			"updated_at": cache.updatedAt,
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"updated_at": cache.updatedAt,
	})
}

// scrapeUnhealthyReason reports why the latest Staub reading is not usable.
// Caller must hold cache.RLock.
func scrapeUnhealthyReason() string {
	if cache.err != "" {
		return cache.err
	}
	if len(cache.data) == 0 || strings.TrimSpace(cache.data[0].TankName) == "" {
		return "no successful scrape"
	}
	if cache.updatedAt.IsZero() || time.Since(cache.updatedAt) > scrapeFreshness {
		return "scrape data is stale"
	}
	return ""
}

func authorize(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Content-Type", "application/json")
	configured := os.Getenv("API_TOKEN")
	if configured == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "api token is not configured"})
		return false
	}
	got, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !tokenMatches(got, configured) {
		log.Printf("unauthorized %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := header[len(prefix):]
	if token == "" || strings.Contains(token, " ") {
		return "", false
	}
	return token, true
}

func tokenMatches(got, want string) bool {
	sumGot := sha256.Sum256([]byte(got))
	sumWant := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(sumGot[:], sumWant[:]) == 1
}

func healthcheck(url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || body.Status != "ok" {
		return fmt.Errorf("status %d %q", resp.StatusCode, body.Status)
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		if err := healthcheck("http://127.0.0.1:5123/health"); err != nil {
			log.Printf("health check: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Getenv("API_TOKEN") == "" {
		log.Println("API_TOKEN is unset; tank routes will reject requests")
	}
	go poll()
	http.HandleFunc("/tanks", tanksHandler)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/health/scrape", scrapeHealthHandler)
	log.Println("Listening on :5123")
	log.Fatal(http.ListenAndServe(":5123", nil))
}
