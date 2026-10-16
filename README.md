# skoda-exporter

Prometheus exporter for the [MyŠkoda public API](https://public.api.connect.skoda-auto.cz/docs).
It polls `GET /api/v1/vehicles/{vin}` for the configured VIN in the background and
serves the latest result on `/metrics`. Scrapes never call the API, so the scrape
interval doesn't affect the API's limit of 20 calls per hour per VIN.

## Quick start

1. Create an API key in the MyŠkoda app (8.16 or later) for the vehicles you want.
2. Run:

```sh
echo -n 'your-key' > api-key.txt
SKODA_API_KEY_FILE=api-key.txt SKODA_VIN=TMB... go run ./cmd/skoda-exporter
curl localhost:9877/metrics
```

Or with Docker Compose (exporter + Prometheus with alert rules):

```sh
cp api-key.txt deploy/
# edit SKODA_VIN in deploy/docker-compose.yml
docker compose -f deploy/docker-compose.yml up -d
```

## Kubernetes with FluxCD

`deploy/kubernetes` is a Kustomize base for clusters running the Prometheus Operator:
namespace, ConfigMap, Deployment, Service, ServiceMonitor and PrometheusRule.
`deploy/kubernetes/flux/skoda-exporter.yaml` holds the Flux `GitRepository` and
`Kustomization` that apply it.

1. Push a `v*` tag so CI publishes `ghcr.io/OWNER/skoda-exporter:<version>`, then set
   that image in `deployment.yaml` (replace `OWNER`). If the package is private, add an
   `imagePullSecrets` entry.
2. Put your VIN in `configmap.yaml`.
3. Create the API key secret with SOPS (Flux decrypts it with the `sops-age` secret):
   ```sh
   cp deploy/kubernetes/secret.example.yaml deploy/kubernetes/secret.sops.yaml
   # edit the key in, then
   sops --encrypt --in-place deploy/kubernetes/secret.sops.yaml
   ```
   and uncomment `secret.sops.yaml` in `kustomization.yaml`.
4. Set the `release` label on the ServiceMonitor and PrometheusRule to whatever your
   Prometheus resource selects on (`kube-prometheus-stack` by default).
5. Add `flux/skoda-exporter.yaml` to your fleet repo, with the repo URL filled in.

Keep `replicas: 1`: each replica polls the API on its own.

## Configuration

Every option is an environment variable and a flag.

| Env var | Flag | Default | Meaning |
| --- | --- | --- | --- |
| `SKODA_API_KEY_FILE` | `-api-key-file` | | File holding the API key (preferred) |
| `SKODA_API_KEY` | `-api-key` | | API key, if no file is used |
| `SKODA_VIN` | `-vin` | | VIN to poll (required) |
| `SKODA_POLL_INTERVAL` | `-poll-interval` | `5m` | Time between calls per VIN; raised to `4m` if lower |
| `SKODA_INCLUDE` | `-include` | all | Comma-separated response parts to request |
| `SKODA_BASE_URL` | `-base-url` | production | Use `https://public.test-api.connect.skoda-auto.cz/api/v1` for the sandbox |
| `SKODA_STALE_AFTER` | `-stale-after` | `1h` | Drop vehicle metrics when data is older |
| `LISTEN_ADDR` | `-listen-address` | `:9877` | Address for `/metrics` and `/healthz` |
| `LOG_LEVEL` | `-log-level` | `info` | `debug` logs missing response parts |

## Behaviour

- **Missing data.** The API may omit parts it can't deliver and list them in `errors`.
  Missing fields produce no series (never a 0); the error types appear in
  `skoda_api_part_errors`.
- **Failures.** A failed call sets `skoda_up` to 0 and keeps the last data until
  `SKODA_STALE_AFTER`. 5xx errors wait for the next regular tick, because failed
  calls count against the quota. 401/403 back off to one call per hour.
- **Rate limit.** On 429 the exporter waits for `Retry-After`; when
  `RateLimit-Remaining` reaches 0 it waits for `RateLimit-Reset`.

## Metrics

All vehicle metrics are gauges with a `vin` label.

| Metric | Source |
| --- | --- |
| `skoda_vehicle_info{name,license_plate}` | always 1 |
| `skoda_odometer_meters` | `odometer.mileageInKm` |
| `skoda_battery_soc_ratio` | `charging.status.battery.stateOfChargeInPercent` / 100 |
| `skoda_battery_range_meters` | `charging.status.battery.remainingCruisingRangeInMeters` |
| `skoda_charging_power_watts` | `charging.status.chargePowerInKw` |
| `skoda_charging_rate_meters_per_hour` | `charging.status.chargingRateInKilometersPerHour` |
| `skoda_charging_remaining_seconds` | `charging.status.remainingTimeToFullyChargedInMinutes` |
| `skoda_charging_fully_charged_timestamp_seconds` | `charging.status.fullyChargedAt` |
| `skoda_charging_state{state}` | `charging.status.state`, 1 for the current value |
| `skoda_charging_type{type}` | `charging.status.chargeType`, 1 for the current value |
| `skoda_charging_plug_connected` | `charging.status.plugConnectionState` |
| `skoda_charging_plug_locked` | `charging.status.plugLockState` |
| `skoda_charging_target_soc_ratio` | `charging.settings.targetStateOfChargeInPercent` / 100 |
| `skoda_charging_max_current_amperes` | `charging.settings.maxChargeCurrentAcAmpere` |
| `skoda_charging_profile_target_soc_ratio` | `chargingProfiles.currentVehiclePositionProfile.targetStateOfChargeInPercent` / 100 |
| `skoda_charging_in_saved_location` | `charging.isVehicleInSavedLocation` |
| `skoda_air_conditioning_state{state}` | `airConditioning.state`, 1 for the current value |
| `skoda_doors_locked` | `status.overall.doorsLocked` |

Exporter health:

| Metric | Meaning |
| --- | --- |
| `skoda_up` | 1 if the last call for the VIN succeeded |
| `skoda_last_success_timestamp_seconds` | Time of the last successful call |
| `skoda_api_requests_total{code}` | API calls by HTTP status (`error` for network failures) |
| `skoda_api_part_errors{type}` | Parts missing from the last response |
| `skoda_api_ratelimit_remaining` | From `RateLimit-Remaining` |
| `skoda_api_key_expiry_timestamp_seconds` | From `X-API-Key-Expires-At` |

`deploy/alerts.yml` has alerts for key expiry within 7 days, stale data and an exhausted quota.

## Known gaps

The field names come from the API docs page, its changelog and evcc's client; the
OpenAPI spec itself has not been checked yet. Not exported yet: window and light
status, fuel level, auxiliary heating, active ventilation and parking position
(position will be opt-in).

## Development

```sh
go test -race ./...
```

Collector output is checked against `internal/collector/testdata/full.prom`.
