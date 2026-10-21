package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	baseURL = "https://edstaub.myfuelportal.com"

	pollInterval    = 2 * time.Hour
	scrapeFreshness = pollInterval + 15*time.Minute
)

var (
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

func newBrowser() (context.Context, context.CancelFunc) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-setuid-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)
	if os.Getenv("DEBUG") == "1" {
		opts = append(opts, chromedp.Flag("headless", false))
	}
	if p := os.Getenv("CHROME_PATH"); p != "" {
		opts = append(opts, chromedp.ExecPath(p))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, ctxCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))
	return ctx, func() { ctxCancel(); allocCancel() }
}

func step(name string) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		log.Printf("step: %s", name)
		return nil
	})
}

func scrape() ([]Tank, error) {
	ctx, cancel := newBrowser()
	defer cancel()

	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var gallonsText, percentText, tankName string

	err := chromedp.Run(ctx,
		step("navigate to login"),
		chromedp.Navigate(baseURL+"/Account/Login"),
		step("wait for email field"),
		chromedp.WaitReady(`input[name="EmailAddress"][type="email"]`),
		step("fill credentials"),
		chromedp.SendKeys(`input[name="EmailAddress"][type="email"]`, email),
		chromedp.SendKeys(`input[name="Password"]`, password),
		step("submit login"),
		chromedp.Click(`button[type="submit"]`),
		step("wait for post-login page"),
		chromedp.WaitVisible(`h3.box-title`),
		step("navigate to tank page"),
		chromedp.Navigate(baseURL+"/Tank"),
		step("wait for progress bar"),
		chromedp.WaitVisible(`div.progress-bar`),
		step("extract data"),
		chromedp.Text(`span.text-larger`, &tankName),
		chromedp.AttributeValue(`div.progress-bar`, "aria-valuenow", &percentText, nil),
		chromedp.Evaluate(`
			(() => {
				for (const d of document.querySelectorAll('div')) {
					if (d.textContent.includes('Approximately') && d.textContent.includes('gallons')) {
						return d.textContent.trim();
					}
				}
				return '';
			})()
		`, &gallonsText),
	)
	if err != nil {
		return nil, err
	}

	tank, err := tankFromExtract(tankName, percentText, gallonsText)
	if err != nil {
		return nil, err
	}
	return []Tank{tank}, nil
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

func main() {
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
