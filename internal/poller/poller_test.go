package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"skoda-exporter/internal/api"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNextDelay(t *testing.T) {
	zero, five := 0, 5
	const iv = 5 * time.Minute
	for _, tc := range []struct {
		name string
		meta api.Meta
		err  error
		want time.Duration
	}{
		{"success", api.Meta{StatusCode: 200, RateLimitRemaining: &five}, nil, iv},
		{"quota used up on success", api.Meta{StatusCode: 200, RateLimitRemaining: &zero, RateLimitReset: 40 * time.Minute}, nil, 40 * time.Minute},
		{"429 honours Retry-After", api.Meta{StatusCode: 429, RetryAfter: 20 * time.Minute}, &api.StatusError{StatusCode: 429}, 20 * time.Minute},
		{"429 short Retry-After keeps interval", api.Meta{StatusCode: 429, RetryAfter: time.Minute}, &api.StatusError{StatusCode: 429}, iv},
		{"huge RateLimit-Reset with quota used up is capped", api.Meta{StatusCode: 200, RateLimitRemaining: &zero, RateLimitReset: 1_700_000_000 * time.Second}, nil, time.Hour},
		{"far-future Retry-After is capped", api.Meta{StatusCode: 429, RetryAfter: 24 * 365 * time.Hour}, &api.StatusError{StatusCode: 429}, time.Hour},
		{"429 without headers keeps interval", api.Meta{StatusCode: 429}, &api.StatusError{StatusCode: 429}, iv},
		{"429 with only RateLimit-Reset", api.Meta{StatusCode: 429, RateLimitReset: 30 * time.Minute}, &api.StatusError{StatusCode: 429}, 30 * time.Minute},
		{"401 backs off", api.Meta{StatusCode: 401}, &api.StatusError{StatusCode: 401}, time.Hour},
		{"403 backs off", api.Meta{StatusCode: 403}, &api.StatusError{StatusCode: 403}, time.Hour},
		{"5xx waits a normal tick", api.Meta{StatusCode: 503}, &api.StatusError{StatusCode: 503}, iv},
		{"network error waits a normal tick", api.Meta{}, errors.New("dial tcp: timeout"), iv},
	} {
		if got := nextDelay(iv, tc.meta, tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// With interval > 1h the interval is the cap.
	huge := api.Meta{StatusCode: 200, RateLimitRemaining: &zero, RateLimitReset: 1_700_000_000 * time.Second}
	if got := nextDelay(2*time.Hour, huge, nil); got != 2*time.Hour {
		t.Errorf("interval above 1h: got %v, want 2h", got)
	}
}

type stubFetcher struct {
	meta api.Meta
	err  error
}

func (s stubFetcher) Vehicle(context.Context, string, []string) (*api.VehicleResponse, api.Meta, error) {
	if s.err != nil {
		return nil, s.meta, s.err
	}
	return &api.VehicleResponse{Vehicle: api.Vehicle{VIN: "V1"}}, s.meta, nil
}

func TestPollOnceCountsAndRecords(t *testing.T) {
	store := NewStore("V1")
	reqs := NewRequestsCounter()
	expiry := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &Poller{VIN: "V1", Store: store, Interval: 5 * time.Minute, Requests: reqs}

	p.Fetcher = stubFetcher{meta: api.Meta{StatusCode: 200, KeyExpiresAt: expiry}}
	p.PollOnce(context.Background())
	p.Fetcher = stubFetcher{meta: api.Meta{StatusCode: 503}, err: &api.StatusError{StatusCode: 503}}
	p.PollOnce(context.Background())
	p.Fetcher = stubFetcher{err: errors.New("network down")}
	p.PollOnce(context.Background())

	for code, want := range map[string]float64{"200": 1, "503": 1, "error": 1} {
		if got := testutil.ToFloat64(reqs.WithLabelValues("V1", code)); got != want {
			t.Errorf("requests{code=%s} = %v, want %v", code, got, want)
		}
	}

	snap := store.Snapshot()
	if snap.Up {
		t.Error("Up should be false after a failure")
	}
	if snap.Response == nil || snap.LastSuccess.IsZero() {
		t.Error("last successful response should be kept")
	}
	if !snap.KeyExpiresAt.Equal(expiry) {
		t.Errorf("KeyExpiresAt = %v", snap.KeyExpiresAt)
	}
}

func TestRunClampsInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Poller{Store: NewStore("V1"), Interval: time.Minute}
	p.Run(ctx)
	if p.Interval != MinInterval {
		t.Errorf("Interval = %v, want %v", p.Interval, MinInterval)
	}
}
