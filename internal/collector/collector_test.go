package collector

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"skoda-exporter/internal/api"
	"skoda-exporter/internal/poller"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestTimestampFormats(t *testing.T) {
	for in, want := range map[string]int{
		"2026-06-12T07:30:00Z": 1,
		"2026-06-12T07:30Z":    1,
		"garbage":              0,
	} {
		ch := make(chan prometheus.Metric, 1)
		ts(ch, descACReach, vin, &in)
		if got := len(ch); got != want {
			t.Errorf("ts(%q) emitted %d, want %d", in, got, want)
		}
	}
}

func TestTemp(t *testing.T) {
	f32, c20 := 32.0, 20.0
	cel, fah, kel := "CELSIUS", "FAHRENHEIT", "KELVIN"
	for name, tc := range map[string]struct {
		in   *api.Temperature
		want []float64
	}{
		"nil":        {nil, nil},
		"fahrenheit": {&api.Temperature{Value: &f32, Unit: &fah}, []float64{0}},
		"celsius":    {&api.Temperature{Value: &c20, Unit: &cel}, []float64{20}},
		"kelvin":     {&api.Temperature{Value: &c20, Unit: &kel}, nil},
		"nil unit":   {&api.Temperature{Value: &c20}, nil},
		"nil value":  {&api.Temperature{Unit: &cel}, nil},
	} {
		ch := make(chan prometheus.Metric, 1)
		temp(ch, descACTemp, vin, tc.in)
		close(ch)
		var got []float64
		for m := range ch {
			var pb dto.Metric
			if err := m.Write(&pb); err != nil {
				t.Fatal(err)
			}
			got = append(got, pb.GetGauge().GetValue())
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}

const vin = "TMBJB9NY5RF999999"

type fakeFetcher struct {
	res  *api.VehicleResponse
	meta api.Meta
	err  error
}

func (f *fakeFetcher) Vehicle(context.Context, string, string) (*api.VehicleResponse, api.Meta, error) {
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
	store := poller.NewStore(vin)
	p := &poller.Poller{VIN: vin, Fetcher: f, Store: store, Interval: 5 * time.Minute, Now: func() time.Time { return t0 }}
	p.PollOnce(context.Background())
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

func TestPartialResponse(t *testing.T) {
	_, c := setup(&fakeFetcher{res: load(t, "partial.json"), meta: api.Meta{StatusCode: 200}})

	want := `
# HELP skoda_api_part_errors Parts missing from the last successful response; 1 per error type.
# TYPE skoda_api_part_errors gauge
skoda_api_part_errors{type="CHARGING_UNAVAILABLE",vin="TMBJB9NY5RF999999"} 1
# HELP skoda_last_success_timestamp_seconds Unix time of the last successful API call.
# TYPE skoda_last_success_timestamp_seconds gauge
skoda_last_success_timestamp_seconds{vin="TMBJB9NY5RF999999"} 1.7919342e+09
# HELP skoda_odometer_meters Odometer reading.
# TYPE skoda_odometer_meters gauge
skoda_odometer_meters{vin="TMBJB9NY5RF999999"} 1.2753e+07
# HELP skoda_up 1 if the last API call for this VIN succeeded.
# TYPE skoda_up gauge
skoda_up{vin="TMBJB9NY5RF999999"} 1
# HELP skoda_vehicle_info Vehicle metadata; always 1.
# TYPE skoda_vehicle_info gauge
skoda_vehicle_info{license_plate="",name="My Enyaq",vin="TMBJB9NY5RF999999"} 1
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestFailureKeepsLastDataUntilStale(t *testing.T) {
	f := &fakeFetcher{res: load(t, "full.json"), meta: api.Meta{StatusCode: 200}}
	p, c := setup(f)

	// A later call fails: up drops to 0, vehicle data stays.
	f.res, f.meta, f.err = nil, api.Meta{StatusCode: 503}, &api.StatusError{StatusCode: 503}
	p.PollOnce(context.Background())

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
		"YES":      {true, true},
		"locked":   {true, true},
		"no":       {false, true},
		"UNLOCKED": {false, true},
		"UNKNOWN":  {false, false},
		"":         {false, false},
	} {
		locked, ok := parseLocked(in)
		if locked != want.locked || ok != want.ok {
			t.Errorf("parseLocked(%q) = %v, %v", in, locked, ok)
		}
	}
}

func TestDoorsLockedAbsentEmitsNoSeries(t *testing.T) {
	res := load(t, "full.json")
	res.Vehicle.Status.Overall.DoorsLocked = nil
	_, c := setup(&fakeFetcher{res: res, meta: api.Meta{StatusCode: 200}})
	if n := testutil.CollectAndCount(c, "skoda_doors_locked"); n != 0 {
		t.Errorf("doors_locked series = %d, want 0", n)
	}
}

func TestDuplicateErrorTypesGather(t *testing.T) {
	res := load(t, "full.json")
	res.Errors = []api.PartError{{Type: "CHARGING_UNAVAILABLE"}, {Type: "CHARGING_UNAVAILABLE"}}
	_, c := setup(&fakeFetcher{res: res, meta: api.Meta{StatusCode: 200}})
	if n := testutil.CollectAndCount(c, "skoda_api_part_errors"); n != 1 {
		t.Errorf("part_errors series = %d, want 1", n)
	}
}
