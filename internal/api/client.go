// Package api is a minimal client for the MyŠkoda public API.
//
// Spec: https://public.api.connect.skoda-auto.cz/docs/myskoda-public-api.yaml
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ProductionURL = "https://public.api.connect.skoda-auto.cz/api/v1"
	SandboxURL    = "https://public.test-api.connect.skoda-auto.cz/api/v1"
)

// maxBody caps how much of a response is read.
const maxBody = 4 << 20

// Client calls the vehicles endpoint. The zero value is not usable; use New.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	now     func() time.Time
}

func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		// Never follow redirects: Go would forward X-API-Key to the new host.
		httpClient = &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    httpClient,
		now:     time.Now,
	}
}

// Meta holds what the response headers say about quota and key, on success
// and on failure alike.
type Meta struct {
	StatusCode int
	// RateLimitRemaining is nil when the header is absent.
	RateLimitRemaining *int
	// RateLimitReset is how long until the quota window resets; 0 if unknown.
	RateLimitReset time.Duration
	// RetryAfter is set on 429 (and any response carrying Retry-After).
	RetryAfter time.Duration
	// KeyExpiresAt is the zero time when the header is absent.
	KeyExpiresAt time.Time
}

// StatusError is returned for any non-2xx response.
type StatusError struct {
	StatusCode int
	Problem    Problem
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("myskoda api: HTTP %d", e.StatusCode)
	if e.Problem.Type != "" {
		msg += " " + e.Problem.Type
	}
	if e.Problem.Title != "" {
		msg += ": " + e.Problem.Title
	}
	return msg
}

// IsStatus reports whether err is a StatusError with the given code.
func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.StatusCode == code
}

// Vehicle fetches one vehicle. include limits the response to the named
// parts; empty means all parts. Meta is filled whenever a response arrived,
// even if err is non-nil.
func (c *Client) Vehicle(ctx context.Context, vin string, include []string) (*VehicleResponse, Meta, error) {
	u := c.baseURL + "/vehicles/" + url.PathEscape(vin)
	if len(include) > 0 {
		u += "?include=" + url.QueryEscape(strings.Join(include, ","))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, Meta{}, err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "skoda-exporter")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, Meta{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	meta := c.parseMeta(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, meta, fmt.Errorf("myskoda api: reading body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		se := &StatusError{StatusCode: resp.StatusCode}
		_ = json.Unmarshal(body, &se.Problem) // best effort
		return nil, meta, se
	}

	var res VehicleResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, meta, fmt.Errorf("myskoda api: decoding response: %w", err)
	}
	return &res, meta, nil
}

func (c *Client) parseMeta(resp *http.Response) Meta {
	m := Meta{StatusCode: resp.StatusCode}
	h := resp.Header

	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("RateLimit-Remaining"))); err == nil {
		m.RateLimitRemaining = &n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("RateLimit-Reset"))); err == nil && n > 0 {
		m.RateLimitReset = time.Duration(n) * time.Second
	}
	m.RetryAfter = parseRetryAfter(h.Get("Retry-After"), c.now())
	m.KeyExpiresAt = parseTimestamp(h.Get("X-API-Key-Expires-At"))
	return m
}

// parseRetryAfter accepts delay-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// parseTimestamp accepts RFC 3339, an HTTP date, or Unix seconds, because the
// header's format is not confirmed against the spec yet.
func parseTimestamp(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	if t, err := http.ParseTime(v); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0)
	}
	return time.Time{}
}
