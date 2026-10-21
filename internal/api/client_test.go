package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVehicleFull(t *testing.T) {
	body := fixture(t, "full.json")
	var gotKey, gotPath, gotInclude string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		gotPath = r.URL.Path
		gotInclude = r.URL.Query().Get("include")
		w.Header().Set("RateLimit-Remaining", "17")
		w.Header().Set("RateLimit-Reset", "1800")
		w.Header().Set("X-API-Key-Expires-At", "2027-01-01T00:00:00Z")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := New(srv.URL+"/api/v1", "secret")
	res, meta, err := c.Vehicle(context.Background(), "TMBJB9NY5RF999999", "charging,odometer")
	if err != nil {
		t.Fatal(err)
	}

	if gotKey != "secret" {
		t.Errorf("X-API-Key = %q", gotKey)
	}
	if gotPath != "/api/v1/vehicles/TMBJB9NY5RF999999" {
		t.Errorf("path = %q", gotPath)
	}
	if gotInclude != "charging,odometer" {
		t.Errorf("include = %q", gotInclude)
	}

	if meta.RateLimitRemaining == nil || *meta.RateLimitRemaining != 17 {
		t.Errorf("RateLimitRemaining = %v", meta.RateLimitRemaining)
	}
	if meta.RateLimitReset != 30*time.Minute {
		t.Errorf("RateLimitReset = %v", meta.RateLimitReset)
	}
	if want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); !meta.KeyExpiresAt.Equal(want) {
		t.Errorf("KeyExpiresAt = %v", meta.KeyExpiresAt)
	}

	if got := *res.Vehicle.Charging.Status.Battery.StateOfChargeInPercent; got != 71 {
		t.Errorf("soc = %v", got)
	}
}

func TestVehicleDoesNotFollowRedirects(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()

	_, _, err := New(srv.URL, "secret").Vehicle(context.Background(), "V1", "")
	if !IsStatus(err, http.StatusFound) {
		t.Errorf("err = %v, want 302 StatusError", err)
	}
	if hit {
		t.Error("redirect target received a request")
	}
}

func TestVehiclePartial(t *testing.T) {
	body := fixture(t, "partial.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query %q", r.URL.RawQuery)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	res, _, err := New(srv.URL, "k").Vehicle(context.Background(), "VIN", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Vehicle.Charging != nil {
		t.Error("charging should be nil")
	}
	if len(res.Errors) != 1 || res.Errors[0].Type != "CHARGING_UNAVAILABLE" {
		t.Errorf("errors = %+v", res.Errors)
	}
}

func TestVehicleErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		header     map[string]string
		body       string
		wantType   string
		retryAfter time.Duration
	}{
		{
			name:     "expired key",
			status:   401,
			body:     `{"type":"https://public.api.connect.skoda-auto.cz/problems/api-key-expired","title":"API key expired","status":401}`,
			wantType: "https://public.api.connect.skoda-auto.cz/problems/api-key-expired",
		},
		{
			name:   "not authorized",
			status: 403,
			body:   `{"type":"api-key-not-authorized"}`, wantType: "api-key-not-authorized",
		},
		{
			name:       "rate limited",
			status:     429,
			header:     map[string]string{"Retry-After": "120", "RateLimit-Remaining": "0"},
			retryAfter: 2 * time.Minute,
		},
		{
			name:   "server error without problem body",
			status: 503,
			body:   "upstream unavailable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, meta, err := New(srv.URL, "k").Vehicle(context.Background(), "VIN", "")
			if !IsStatus(err, tc.status) {
				t.Fatalf("err = %v, want status %d", err, tc.status)
			}
			if meta.StatusCode != tc.status {
				t.Errorf("meta.StatusCode = %d", meta.StatusCode)
			}
			if se := err.(*StatusError); se.Problem.Type != tc.wantType {
				t.Errorf("problem type = %q", se.Problem.Type)
			}
			if meta.RetryAfter != tc.retryAfter {
				t.Errorf("RetryAfter = %v", meta.RetryAfter)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"":                              0,
		"30":                            30 * time.Second,
		"-5":                            0,
		"Tue, 13 Oct 2026 12:01:00 GMT": time.Minute,
		"Tue, 13 Oct 2026 11:00:00 GMT": 0,
		"garbage":                       0,
	} {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, in := range []string{"2027-01-01T00:00:00Z", "Fri, 01 Jan 2027 00:00:00 GMT", "1798761600"} {
		if got := parseTimestamp(in); !got.Equal(want) {
			t.Errorf("parseTimestamp(%q) = %v", in, got)
		}
	}
	if got := parseTimestamp("nope"); !got.IsZero() {
		t.Errorf("parseTimestamp(nope) = %v", got)
	}
}

func TestDecodeRealResponse(t *testing.T) {
	var res VehicleResponse
	if err := json.Unmarshal(fixture(t, "real.json"), &res); err != nil {
		t.Fatal(err)
	}
	v := res.Vehicle
	if got := *v.Status.Overall.DoorsLocked; got != "YES" {
		t.Errorf("doorsLocked = %q", got)
	}
	if got := *v.Odometer.MileageInKm; got != 41645 {
		t.Errorf("mileage = %v", got)
	}
	if got := *v.Charging.Status.Battery.StateOfChargeInPercent; got != 38 {
		t.Errorf("soc = %v", got)
	}
	if got := *v.Charging.Status.Battery.RemainingCruisingRangeInMeters; got != 158000 {
		t.Errorf("range = %v", got)
	}
	if got := *v.Charging.Status.State; got != "CONNECT_CABLE" {
		t.Errorf("state = %q", got)
	}
}
