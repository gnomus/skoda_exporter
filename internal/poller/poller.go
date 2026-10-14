// Package poller fetches each vehicle on a timer and keeps the latest result,
// so Prometheus scrapes never spend API quota.
package poller

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"skoda-exporter/internal/api"

	"github.com/prometheus/client_golang/prometheus"
)

// MinInterval keeps one VIN at 15 calls per hour, under the API's 20, with
// room for failed calls, which also count.
const MinInterval = 4 * time.Minute

// authBackoff is the delay after a 401 or 403; those don't count against the
// quota, but they won't fix themselves quickly either.
const authBackoff = time.Hour

// Fetcher is the part of api.Client the poller needs.
type Fetcher interface {
	Vehicle(ctx context.Context, vin string, include []string) (*api.VehicleResponse, api.Meta, error)
}

// Snapshot is what the collector reads for one VIN.
type Snapshot struct {
	VIN string
	// Response is the last successful response; nil before the first one.
	Response    *api.VehicleResponse
	LastSuccess time.Time
	// Up is true if the most recent call succeeded.
	Up                 bool
	RateLimitRemaining *int
}

// Store holds the latest state for all VINs.
type Store struct {
	mu           sync.RWMutex
	vins         map[string]*Snapshot
	keyExpiresAt time.Time
}

func NewStore(vins []string) *Store {
	s := &Store{vins: make(map[string]*Snapshot, len(vins))}
	for _, v := range vins {
		s.vins[v] = &Snapshot{VIN: v}
	}
	return s
}

// Snapshots returns copies of all VIN states.
func (s *Store) Snapshots() []Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Snapshot, 0, len(s.vins))
	for _, v := range s.vins {
		out = append(out, *v)
	}
	return out
}

// KeyExpiresAt returns the API key expiry, or the zero time if never seen.
func (s *Store) KeyExpiresAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keyExpiresAt
}

func (s *Store) record(vin string, res *api.VehicleResponse, meta api.Meta, err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.vins[vin]
	if snap == nil {
		snap = &Snapshot{VIN: vin}
		s.vins[vin] = snap
	}
	snap.Up = err == nil
	if err == nil {
		snap.Response = res
		snap.LastSuccess = now
	}
	if meta.RateLimitRemaining != nil {
		n := *meta.RateLimitRemaining
		snap.RateLimitRemaining = &n
	}
	if !meta.KeyExpiresAt.IsZero() {
		s.keyExpiresAt = meta.KeyExpiresAt
	}
}

// Poller runs one loop per VIN.
type Poller struct {
	Fetcher  Fetcher
	Store    *Store
	VINs     []string
	Include  []string
	Interval time.Duration
	Logger   *slog.Logger
	// Requests counts API calls by VIN and HTTP status ("error" if none).
	Requests *prometheus.CounterVec
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// NewRequestsCounter returns the counter the poller expects in Requests.
func NewRequestsCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "skoda_api_requests_total",
		Help: "MyŠkoda API calls by VIN and HTTP status code.",
	}, []string{"vin", "code"})
}

// Run blocks until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	if p.Interval < MinInterval {
		p.Interval = MinInterval
	}
	var wg sync.WaitGroup
	for _, vin := range p.VINs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.loop(ctx, vin)
		}()
	}
	wg.Wait()
}

func (p *Poller) loop(ctx context.Context, vin string) {
	// Spread VINs out so they don't all call at once.
	delay := time.Duration(rand.Int64N(int64(30 * time.Second)))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = p.PollOnce(ctx, vin)
	}
}

// PollOnce fetches one VIN, records the result and returns how long to wait
// before the next call.
func (p *Poller) PollOnce(ctx context.Context, vin string) time.Duration {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}

	res, meta, err := p.Fetcher.Vehicle(ctx, vin, p.Include)
	if ctx.Err() != nil {
		return 0
	}

	code := "error"
	if meta.StatusCode != 0 {
		code = strconv.Itoa(meta.StatusCode)
	}
	if p.Requests != nil {
		p.Requests.WithLabelValues(vin, code).Inc()
	}

	p.Store.record(vin, res, meta, err, now())

	if err != nil {
		p.logger().Warn("vehicle poll failed", "vin", vin, "err", err)
	} else if len(res.Errors) > 0 {
		types := make([]string, 0, len(res.Errors))
		for _, e := range res.Errors {
			types = append(types, e.Type)
		}
		p.logger().Debug("vehicle poll partial", "vin", vin, "missing", types)
	}

	return nextDelay(p.Interval, meta, err)
}

func nextDelay(interval time.Duration, meta api.Meta, err error) time.Duration {
	d := interval
	switch {
	case api.IsStatus(err, http.StatusUnauthorized), api.IsStatus(err, http.StatusForbidden):
		d = max(d, authBackoff)
	case api.IsStatus(err, http.StatusTooManyRequests):
		d = max(d, meta.RetryAfter, meta.RateLimitReset)
	}
	// Quota used up, even on a success: wait for the window to reset.
	if meta.RateLimitRemaining != nil && *meta.RateLimitRemaining <= 0 {
		d = max(d, meta.RateLimitReset)
	}
	return d
}

func (p *Poller) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}
