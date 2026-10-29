package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const testToken = "test-token"

func resetCache(data []Tank, err string, updated time.Time) {
	cache.Lock()
	defer cache.Unlock()
	cache.data = data
	cache.err = err
	cache.updatedAt = updated
}

func authedRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	return req
}

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, rr.Body.Bytes())
	}
	return body
}

func TestHealthAlwaysReturnsJSON(t *testing.T) {
	t.Setenv("API_TOKEN", testToken)
	resetCache(nil, "login failed", time.Time{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	healthHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["status"] != "ok" {
		t.Fatalf("status field = %#v, want ok", body["status"])
	}
}

func TestScrapeHealth(t *testing.T) {
	fresh := time.Now().UTC()
	stale := fresh.Add(-scrapeFreshness - time.Minute)
	reading := []Tank{{TankName: "House", Gallons: 1200, Percent: 40}}

	tests := []struct {
		name       string
		data       []Tank
		err        string
		updated    time.Time
		wantStatus int
		wantState  string
		wantErr    string
	}{
		{
			name:       "never scraped",
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "fail",
			wantErr:    "no successful scrape",
		},
		{
			name:       "last scrape failed",
			data:       reading,
			err:        "login failed",
			updated:    fresh,
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "fail",
			wantErr:    "login failed",
		},
		{
			name:       "reading is stale",
			data:       reading,
			updated:    stale,
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "fail",
			wantErr:    "scrape data is stale",
		},
		{
			name:       "empty tank name",
			data:       []Tank{{Gallons: 10, Percent: 5}},
			updated:    fresh,
			wantStatus: http.StatusServiceUnavailable,
			wantState:  "fail",
			wantErr:    "no successful scrape",
		},
		{
			name:       "fresh reading",
			data:       reading,
			updated:    fresh,
			wantStatus: http.StatusOK,
			wantState:  "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("API_TOKEN", testToken)
			resetCache(tt.data, tt.err, tt.updated)

			req := authedRequest(http.MethodGet, "/health/scrape")
			rr := httptest.NewRecorder()
			scrapeHealthHandler(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
			body := decodeJSON(t, rr)
			if body["status"] != tt.wantState {
				t.Fatalf("status field = %#v, want %s", body["status"], tt.wantState)
			}
			if _, ok := body["updated_at"]; !ok {
				t.Fatal("response missing updated_at")
			}
			if tt.wantErr != "" && body["error"] != tt.wantErr {
				t.Fatalf("error = %#v, want %s", body["error"], tt.wantErr)
			}
		})
	}
}

func TestTanksServesLastReadingAfterScrapeError(t *testing.T) {
	t.Setenv("API_TOKEN", testToken)
	updated := time.Now().UTC().Add(-time.Hour)
	resetCache([]Tank{{TankName: "House", Gallons: 1200, Percent: 40}}, "login failed", updated)

	req := authedRequest(http.MethodGet, "/tanks")
	rr := httptest.NewRecorder()
	tanksHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := decodeJSON(t, rr)
	if body["stale"] != true {
		t.Fatalf("stale = %#v, want true", body["stale"])
	}
	tanks, ok := body["tanks"].([]any)
	if !ok || len(tanks) != 1 {
		t.Fatalf("tanks = %#v, want one reading", body["tanks"])
	}
}

func TestTankFromExtract(t *testing.T) {
	tank, err := tankFromExtract("  House  ", "41.6", "Approximately 1,250 gallons")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tank.TankName != "House" || tank.Gallons != 1250 || tank.Percent != 42 {
		t.Fatalf("tank = %+v", tank)
	}

	if _, err := tankFromExtract("  ", "10", "Approximately 10 gallons"); err == nil {
		t.Fatal("expected error for empty tank name")
	}
	if _, err := tankFromExtract("House", "10", "tank level unavailable"); err == nil {
		t.Fatal("expected error when gallons text does not match")
	}
}

func TestProtectedRoutesRequireToken(t *testing.T) {
	resetCache([]Tank{{TankName: "House", Gallons: 1200, Percent: 40}}, "", time.Now().UTC())
	routes := []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"/tanks", tanksHandler},
		{"/health/scrape", scrapeHealthHandler},
	}

	t.Run("unset token", func(t *testing.T) {
		t.Setenv("API_TOKEN", "")
		for _, route := range routes {
			req := httptest.NewRequest(http.MethodGet, route.path, nil)
			rr := httptest.NewRecorder()
			route.handler(rr, req)
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s status = %d, want 503", route.path, rr.Code)
			}
			body := decodeJSON(t, rr)
			if body["error"] != "api token is not configured" {
				t.Fatalf("%s error = %#v", route.path, body["error"])
			}
		}
	})

	t.Run("missing bearer", func(t *testing.T) {
		t.Setenv("API_TOKEN", testToken)
		logs := captureLog(t)
		for _, route := range routes {
			req := httptest.NewRequest(http.MethodGet, route.path, nil)
			rr := httptest.NewRecorder()
			route.handler(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("%s status = %d, want 401", route.path, rr.Code)
			}
			body := decodeJSON(t, rr)
			if body["error"] != "unauthorized" {
				t.Fatalf("%s error = %#v", route.path, body["error"])
			}
		}
		assertUnauthorizedLog(t, logs.String(), "")
	})

	t.Run("wrong bearer", func(t *testing.T) {
		t.Setenv("API_TOKEN", testToken)
		supplied := "not-the-token"
		logs := captureLog(t)
		for _, route := range routes {
			req := httptest.NewRequest(http.MethodGet, route.path, nil)
			req.Header.Set("Authorization", "Bearer "+supplied)
			rr := httptest.NewRecorder()
			route.handler(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("%s status = %d, want 401", route.path, rr.Code)
			}
			body := decodeJSON(t, rr)
			if body["error"] != "unauthorized" {
				t.Fatalf("%s error = %#v", route.path, body["error"])
			}
			if strings.Contains(rr.Body.String(), supplied) {
				t.Fatalf("%s response includes the supplied token", route.path)
			}
		}
		assertUnauthorizedLog(t, logs.String(), supplied)
	})
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func assertUnauthorizedLog(t *testing.T, logs, supplied string) {
	t.Helper()
	for _, path := range []string{"/tanks", "/health/scrape"} {
		if !strings.Contains(logs, "unauthorized GET "+path) {
			t.Fatalf("log missing unauthorized line for %s: %s", path, logs)
		}
	}
	if strings.Contains(logs, testToken) {
		t.Fatal("log includes API token")
	}
	if supplied != "" && strings.Contains(logs, supplied) {
		t.Fatal("log includes the supplied token")
	}
}

func TestHealthcheck(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(healthHandler))
	t.Cleanup(okSrv.Close)
	if err := healthcheck(okSrv.URL + "/health"); err != nil {
		t.Fatal(err)
	}

	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "fail"})
	}))
	t.Cleanup(badSrv.Close)
	if err := healthcheck(badSrv.URL); err == nil {
		t.Fatal("expected healthcheck to fail")
	}
}

func loadDoc(t *testing.T, path string) *goquery.Document {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := goquery.NewDocumentFromReader(f)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestParseTanksSkipsRowsWithoutGauge(t *testing.T) {
	tanks, err := parseTanks(loadDoc(t, "testdata/tank.html"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Tank{{TankName: "7SF000000-1000", Gallons: 660, Percent: 66}}
	if len(tanks) != len(want) || tanks[0] != want[0] {
		t.Fatalf("tanks = %+v, want %+v", tanks, want)
	}
}

func TestParseTanksNoReadings(t *testing.T) {
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(`<div class="tank-row"><span class="text-larger">X</span></div>`))
	if _, err := parseTanks(doc); err == nil {
		t.Fatal("expected error when no tank has a reading")
	}
}

const loginForm = `<form action="/Account/Login" method="post"><input name="__RequestVerificationToken" type="hidden" value="tok123" /></form>`

// fakePortal mimics the portal's login flow: a session cookie is issued only
// when the form is posted with the right token and credentials.
func fakePortal(t *testing.T) *httptest.Server {
	t.Helper()
	tankPage, err := os.ReadFile("testdata/tank.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /Account/Login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "__RequestVerificationToken", Value: "cookie-tok", Path: "/"})
		w.Write([]byte(loginForm))
	})
	mux.HandleFunc("POST /Account/Login", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("__RequestVerificationToken")
		if err != nil || c.Value != "cookie-tok" || r.FormValue("__RequestVerificationToken") != "tok123" {
			http.Error(w, "bad token", http.StatusInternalServerError)
			return
		}
		if r.FormValue("EmailAddress") != "me@example.com" || r.FormValue("Password") != "secret" {
			w.Write([]byte(loginForm))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: ".ASPXAUTH", Value: "session", Path: "/"})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<h3 class="box-title">Account Overview</h3>`))
	})
	mux.HandleFunc("GET /Tank", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(".ASPXAUTH"); err != nil {
			http.Redirect(w, r, "/Account/Login", http.StatusFound)
			return
		}
		w.Write(tankPage)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func withPortal(t *testing.T, srv *httptest.Server, user, pass string) {
	t.Helper()
	oldURL, oldEmail, oldPass := baseURL, email, password
	baseURL, email, password = srv.URL, user, pass
	t.Cleanup(func() { baseURL, email, password = oldURL, oldEmail, oldPass })
}

func TestScrapeLogsInAndReadsTanks(t *testing.T) {
	withPortal(t, fakePortal(t), "me@example.com", "secret")
	tanks, err := scrape()
	if err != nil {
		t.Fatal(err)
	}
	if len(tanks) != 1 || tanks[0].Gallons != 660 {
		t.Fatalf("tanks = %+v", tanks)
	}
}

func TestScrapeBadCredentials(t *testing.T) {
	withPortal(t, fakePortal(t), "me@example.com", "wrong")
	if _, err := scrape(); !errors.Is(err, errLoginRequired) {
		t.Fatalf("err = %v, want errLoginRequired", err)
	}
}
