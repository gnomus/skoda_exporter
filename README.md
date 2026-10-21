# skoda-exporter

Prometheus exporter for the [MyŠkoda public API](https://public.api.connect.skoda-auto.cz/docs).
It polls `GET /api/v1/vehicles/{vin}` for the configured VIN in the background and
serves the latest result on `/metrics`. Scrapes never call the API, so the scrape
interval doesn't affect the API's limit of 20 calls per hour per VIN.

## Quick start

1. Create an API key in the MyŠkoda app (8.16 or later) for the vehicles you want.
2. Run:

```sh
SKODA_API_KEY=your-key SKODA_VIN=TMB... go run ./cmd/skoda-exporter
curl localhost:9877/metrics
```

## Docker image

Pushing a semver tag such as `1.2.3` (no `v` prefix) makes CI publish `ghcr.io/OWNER/skoda_exporter:<version>`.
Run a single instance per VIN: each instance polls the API on its own.

## Configuration

Configuration is read from environment variables only.

| Env var | Default | Meaning |
| --- | --- | --- |
| `SKODA_API_KEY` | | API key (required) |
| `SKODA_VIN` | | VIN to poll (required) |
| `SKODA_POLL_INTERVAL` | `5m` | Time between calls; raised to `4m` if lower |
| `SKODA_INCLUDE` | all | Comma-separated response parts to request |
| `SKODA_BASE_URL` | production | Use `https://public.test-api.connect.skoda-auto.cz/api/v1` for the sandbox |
| `SKODA_STALE_AFTER` | `1h` | Drop vehicle metrics when data is older |
| `LISTEN_ADDR` | `:9877` | Address for `/metrics` and `/healthz` |
| `LOG_LEVEL` | `info` | `debug` logs missing response parts |

## Behaviour

- **Missing data.** The API may omit parts it can't deliver and list them in `errors`.
  Missing fields produce no series (never a 0); the error types appear in
  `skoda_api_part_errors`.
- **Failures.** A failed call sets `skoda_up` to 0 and keeps the last data until
  `SKODA_STALE_AFTER`. 5xx errors wait for the next regular tick, because failed
  calls count against the quota. 401/403 back off to one call per hour.
- **Rate limit.** On 429 the exporter waits for `Retry-After`; when
  `RateLimit-Remaining` reaches 0 it waits for `RateLimit-Reset` (at most 1h, or
  the poll interval if longer).

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
| `skoda_charging_in_saved_location` | `charging.isVehicleInSavedLocation` |
| `skoda_air_conditioning_state{state}` | `airConditioning.state`, 1 for the current value |
| `skoda_air_conditioning_target_temperature_celsius` | `airConditioning.targetTemperature` (Fahrenheit converted) |
| `skoda_air_conditioning_target_reached_timestamp_seconds` | `airConditioning.estimatedReachOfTargetTemperatureAt` |
| `skoda_air_conditioning_without_external_power` | `airConditioning.airConditioningWithoutExternalPower` |
| `skoda_air_conditioning_at_unlock` | `airConditioning.airConditioningAtUnlock` |
| `skoda_window_heating_enabled` | `airConditioning.windowHeating.enabled` |
| `skoda_window_heating_front_state{state}` | `airConditioning.windowHeating.front`, 1 for the current value |
| `skoda_window_heating_rear_state{state}` | `airConditioning.windowHeating.rear`, 1 for the current value |
| `skoda_auxiliary_heating_state{state}` | `auxiliaryHeating.state`, 1 for the current value |
| `skoda_auxiliary_heating_start_mode{mode}` | `auxiliaryHeating.startMode`, 1 for the current value |
| `skoda_auxiliary_heating_duration_seconds` | `auxiliaryHeating.durationInSeconds` |
| `skoda_auxiliary_heating_target_temperature_celsius` | `auxiliaryHeating.targetTemperature` (Fahrenheit converted) |
| `skoda_auxiliary_heating_target_reached_timestamp_seconds` | `auxiliaryHeating.estimatedReachOfTargetTemperatureAt` |
| `skoda_active_ventilation_state{state}` | `activeVentilation.state`, 1 for the current value |
| `skoda_active_ventilation_duration_seconds` | `activeVentilation.durationInSeconds` |
| `skoda_doors_locked` | `status.overall.doorsLocked` |

Exporter health:

| Metric | Meaning |
| --- | --- |
| `skoda_up` | 1 if the last call for the VIN succeeded |
| `skoda_last_success_timestamp_seconds` | Time of the last successful call |
| `skoda_api_requests_total{vin,code}` | API calls by HTTP status (`error` for network failures) |
| `skoda_api_part_errors{type}` | Parts missing from the last response |
| `skoda_api_ratelimit_remaining` | From `RateLimit-Remaining` |
| `skoda_api_key_expiry_timestamp_seconds` | From `X-API-Key-Expires-At` |

## Known gaps

The field names come from the API docs page, its changelog and evcc's client; the
climate fields (air conditioning, window heating, auxiliary heating, active
ventilation) were checked against the OpenAPI spec, the rest have not been yet.
`skoda_air_conditioning_state` emits every known state as 0/1, plus any unknown
current value. Not exported yet: window and light
status, fuel level and parking position
(position will be opt-in).

## Development

```sh
go test -race ./...
```

Collector output is checked against `internal/collector/testdata/full.prom`.

## AI usage

Most of this code, its tests and this README were written by Claude (Anthropic's AI
model) through Claude Code. A human maintainer directs the work, reviews the changes
and merges every pull request. Commits that Claude wrote carry a `Co-Authored-By: Claude`
trailer or have `Claude` as their author.
