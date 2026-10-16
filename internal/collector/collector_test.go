package collector

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"skoda-exporter/internal/api"
	"skoda-exporter/internal/poller"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const vin = "TMBJB9NY5RF999999"

type fakeFetcher struct {
	res  *api.VehicleResponse
	meta api.Meta
	err  error
}

func (f *fakeFetcher) Vehicle(context.Context, string, []string) (*api.VehicleResponse, api.Meta, error) {
	return f.res, f.meta, f.err
}

func load(t *testing.T, name string) *api.VehicleResponse {
	t.Helper()
	b, err := os.ReadFile("../api/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var res api.VehicleResponse
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	return &res
}

var t0 = time.Date(2026, 10, 13, 23, 30, 0, 0, time.UTC)

// setup polls once through a fake fetcher and returns a collector over it.
func setup(f *fakeFetcher) (*poller.Poller, *Collector) {
	store := poller.NewStore([]string{vin})
	p := &poller.Poller{Fetcher: f, Store: store, Interval: 5 * time.Minute, Now: func() time.Time { return t0 }}
	p.PollOnce(context.Background(), vin)
	return p, &Collector{Store: store, StaleAfter: time.Hour, Now: func() time.Time { return t0 }}
}

func TestFullResponse(t *testing.T) {
	remaining := 17
	_, c := setup(&fakeFetcher{
		res:  load(t, "full.json"),
		meta: api.Meta{StatusCode: 200, RateLimitRemaining: &remaining, KeyExpiresAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
	})

	f, err := os.Open("testdata/full.prom")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := testutil.CollectAndCompare(c, f); err != nil {
		t.Error(err)
	}
}

func TestPartialResponseHasNoChargingSeries(t *testing.T) {
	_, c := setup(&fakeFetcher{res: load(t, "partial.json"), meta: api.Meta{StatusCode: 200}})

	want := `
# HELP skoda_api_part_errors Parts missing from the last successful response; 1 per error type.
# TYPE skoda_api_part_errors gauge
skoda_api_part_errors{type="CHARGING_UNAVAILABLE",vin="TMBJB9NY5RF999999"} 1
# HELP skoda_odometer_meters Odometer reading.
# TYPE skoda_odometer_meters gauge
skoda_odometer_meters{vin="TMBJB9NY5RF999999"} 1.2753e+07
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "skoda_api_part_errors", "skoda_odometer_meters"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(c, "skoda_battery_soc_ratio", "skoda_charging_state", "skoda_charging_power_watts"); n != 0 {
		t.Errorf("got %d charging series, want 0", n)
	}
}

func TestFailureKeepsLastDataUntilStale(t *testing.T) {
	f := &fakeFetcher{res: load(t, "full.json"), meta: api.Meta{StatusCode: 200}}
	p, c := setup(f)

	// A later call fails: up drops to 0, vehicle data stays.
	f.res, f.meta, f.err = nil, api.Meta{StatusCode: 503}, &api.StatusError{StatusCode: 503}
	p.PollOnce(context.Background(), vin)

	want := `
# HELP skoda_up 1 if the last API call for this VIN succeeded.
# TYPE skoda_up gauge
skoda_up{vin="TMBJB9NY5RF999999"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "skoda_up"); err != nil {
		t.Error(err)
	}
	if n := testutil.CollectAndCount(c, "skoda_battery_soc_ratio"); n != 1 {
		t.Errorf("soc series = %d, want 1 while fresh", n)
	}

	// Past StaleAfter the vehicle data is dropped, health stays.
	c.Now = func() time.Time { return t0.Add(61 * time.Minute) }
	if n := testutil.CollectAndCount(c, "skoda_battery_soc_ratio"); n != 0 {
		t.Errorf("soc series = %d, want 0 when stale", n)
	}
	if n := testutil.CollectAndCount(c, "skoda_up", "skoda_last_success_timestamp_seconds"); n != 2 {
		t.Errorf("health series = %d, want 2", n)
	}
}

func TestUnknownEnumValueIsExported(t *testing.T) {
	res := load(t, "full.json")
	s := "SOMETHING_NEW"
	res.Vehicle.Charging.Status.State = &s
	_, c := setup(&fakeFetcher{res: res, meta: api.Meta{StatusCode: 200}})

	// 6 known states at 0 plus the new one at 1.
	if n := testutil.CollectAndCount(c, "skoda_charging_state"); n != 7 {
		t.Errorf("charging_state series = %d, want 7", n)
	}
}

func TestParseLocked(t *testing.T) {
	for in, want := range map[string]struct{ locked, ok bool }{
		`true`:       {true, true},
		`false`:      {false, true},
		`"YES"`:      {true, true},
		`"no"`:       {false, true},
		`"UNLOCKED"`: {false, true},
		`"UNKNOWN"`:  {false, false},
		``:           {false, false},
		`3`:          {false, false},
	} {
		locked, ok := parseLocked(json.RawMessage(in))
		if locked != want.locked || ok != want.ok {
			t.Errorf("parseLocked(%s) = %v, %v", in, locked, ok)
		}
	}
}
