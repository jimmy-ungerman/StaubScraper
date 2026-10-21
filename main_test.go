package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func resetCache(data []Tank, err string, updated time.Time) {
	cache.Lock()
	defer cache.Unlock()
	cache.data = data
	cache.err = err
	cache.updatedAt = updated
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
			resetCache(tt.data, tt.err, tt.updated)

			req := httptest.NewRequest(http.MethodGet, "/health/scrape", nil)
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
	updated := time.Now().UTC().Add(-time.Hour)
	resetCache([]Tank{{TankName: "House", Gallons: 1200, Percent: 40}}, "login failed", updated)

	req := httptest.NewRequest(http.MethodGet, "/tanks", nil)
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
