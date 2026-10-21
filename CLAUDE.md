# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Prometheus exporter for the MyŠkoda public API (`GET /api/v1/vehicles/{vin}`). Go module `skoda-exporter`, one dependency (`prometheus/client_golang`). README.md documents config, metrics and behaviour for users.

## Commands

```sh
go vet ./...                                   # CI step
go test -race ./...                            # CI step
golangci-lint run                              # CI runs golangci-lint-action@v9 with default config (no .golangci.yml)
go test ./internal/poller -run TestNextDelay   # single test
SKODA_API_KEY=... SKODA_VIN=TMB... go run ./cmd/skoda-exporter   # serves :9877/metrics
```

Use `SKODA_BASE_URL=https://public.test-api.connect.skoda-auto.cz/api/v1` for the sandbox. CI tests every push to main and every PR, and builds an amd64 image to ghcr.io only for semver tags such as `1.2.3` (no `v` prefix).

## Architecture

Data flows `api` → `poller.Store` → `collector`, wired in `cmd/skoda-exporter/main.go` (env-var config, HTTP server, signal shutdown).

- **The API quota (20 calls/hour/VIN, failed calls count) drives the design.** Scrapes never call the API: `poller` runs a single VIN on a timer, stores the latest result in `Store`, and `collector` reads snapshots at scrape time. `poller.MinInterval` (4m) is enforced both in `main` and in `Poller.Run`.
- **`internal/api`** — HTTP client and response types. `Vehicle` returns the response plus `Meta` (status code, `RateLimit-*`, `Retry-After`, `X-API-Key-Expires-At`) even on errors; non-2xx becomes `*StatusError`, checked with `api.IsStatus`. All response fields are pointers because the API omits parts it can't deliver (listed in `errors`).
- **`internal/poller`** — `PollOnce` fetches, counts `skoda_api_requests_total`, records into `Store`, and returns the next delay via `nextDelay` (401/403 → 1h backoff; 429 → `Retry-After`/`RateLimit-Reset`; remaining quota 0 → wait for reset; waits capped at max(interval, 1h)). On failure the store keeps the last good response and sets `Up=false`. `Fetcher` interface exists so tests can fake the client.
- **`internal/collector`** — unchecked collector (empty `Describe`) so series appear/disappear with data. Rules: a missing field emits no series (never 0); vehicle metrics are dropped once `LastSuccess` is older than `StaleAfter`; enums emit every known value (0/1) plus any unknown current value; units are converted to base units (km→m, kW→W, %→ratio, min→s).

## Testing

- Fixtures: `internal/api/testdata/{full,partial}.json` (sample API responses, also loaded by collector tests) and `real.json` (anonymized real response). Time is injected via `Now func() time.Time` fields on `Poller` and `Collector`.
- `internal/collector/testdata/full.prom` is the golden output for `full.json`; update it by hand when adding or changing a metric, and keep the metrics table in README.md in sync.

## Notes

- Field names come from the API docs page/changelog and evcc's client, not the OpenAPI spec; `doorsLocked` is a string enum (YES/NO…, confirmed from a real response) — see `parseLocked`.
- README.md refers to a `deploy/` directory (docker-compose, alerts, Kustomize/Flux manifests) that is not in the repo yet.
