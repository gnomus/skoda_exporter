// Package collector turns the poller's cached responses into Prometheus
// metrics at scrape time.
package collector

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"skoda-exporter/internal/api"
	"skoda-exporter/internal/poller"

	"github.com/prometheus/client_golang/prometheus"
)

// Known enum values are always exported (as 0 when not current) so that
// queries and alerts see a stable set of series. A value the API adds later
// is still exported when it occurs.
var (
	chargingStates = []string{"CHARGING", "CONSERVING", "READY_FOR_CHARGING", "CHARGING_INTERRUPTED", "DISCHARGING", "CONNECT_CABLE"}
	chargeTypes    = []string{"AC", "DC"}
)

func desc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc("skoda_"+name, help, append([]string{"vin"}, labels...), nil)
}

var (
	descInfo = desc("vehicle_info", "Vehicle metadata; always 1.", "name", "license_plate")

	descOdometer       = desc("odometer_meters", "Odometer reading.")
	descSoc            = desc("battery_soc_ratio", "Battery state of charge, 0 to 1.")
	descRange          = desc("battery_range_meters", "Remaining electric range.")
	descPower          = desc("charging_power_watts", "Current charging power.")
	descRate           = desc("charging_rate_meters_per_hour", "Range added per hour of charging.")
	descRemaining      = desc("charging_remaining_seconds", "Time until fully charged.")
	descFullyAt        = desc("charging_fully_charged_timestamp_seconds", "When charging is expected to finish, as Unix time.")
	descState          = desc("charging_state", "Charging state; 1 for the current state.", "state")
	descType           = desc("charging_type", "Charge type; 1 for the current type.", "type")
	descPlugConnected  = desc("charging_plug_connected", "1 if the charging cable is connected.")
	descPlugLocked     = desc("charging_plug_locked", "1 if the charging plug is locked.")
	descTargetSoc      = desc("charging_target_soc_ratio", "Global charging limit, 0 to 1.")
	descMaxCurrent     = desc("charging_max_current_amperes", "Maximum AC charging current.")
	descProfileTarget  = desc("charging_profile_target_soc_ratio", "Charging limit of the profile at the vehicle's current location, 0 to 1.")
	descSavedLocation  = desc("charging_in_saved_location", "1 if the vehicle is at a saved charging location.")
	descAirConditioner = desc("air_conditioning_state", "Air conditioning state; 1 for the current state.", "state")
	descDoorsLocked    = desc("doors_locked", "1 if the doors are locked.")

	descUp          = desc("up", "1 if the last API call for this VIN succeeded.")
	descLastSuccess = desc("last_success_timestamp_seconds", "Unix time of the last successful API call.")
	descPartErrors  = desc("api_part_errors", "Parts missing from the last successful response; 1 per error type.", "type")
	descRateLimit   = desc("api_ratelimit_remaining", "Remaining API calls in the current window, from RateLimit-Remaining.")
	descKeyExpiry   = prometheus.NewDesc("skoda_api_key_expiry_timestamp_seconds", "Unix time when the API key expires, from X-API-Key-Expires-At.", nil, nil)
)

// Collector implements prometheus.Collector over a poller.Store. It is an
// unchecked collector: series come and go with the data.
type Collector struct {
	Store *poller.Store
	// StaleAfter drops vehicle metrics when the last success is older.
	StaleAfter time.Duration

	Now func() time.Time
}

func (c *Collector) Describe(chan<- *prometheus.Desc) {}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}

	if t := c.Store.KeyExpiresAt(); !t.IsZero() {
		ch <- prometheus.MustNewConstMetric(descKeyExpiry, prometheus.GaugeValue, unix(t))
	}

	{
		s := c.Store.Snapshot()
		ch <- gauge(descUp, b2f(s.Up), s.VIN)
		if s.RateLimitRemaining != nil {
			ch <- gauge(descRateLimit, float64(*s.RateLimitRemaining), s.VIN)
		}
		if s.Response == nil {
			return
		}
		ch <- gauge(descLastSuccess, unix(s.LastSuccess), s.VIN)
		if c.StaleAfter > 0 && now().Sub(s.LastSuccess) > c.StaleAfter {
			return
		}
		collectVehicle(ch, s.VIN, s.Response)
	}
}

func collectVehicle(ch chan<- prometheus.Metric, vin string, res *api.VehicleResponse) {
	v := res.Vehicle
	ch <- gauge(descInfo, 1, vin, v.Name, v.LicensePlate)

	for _, e := range res.Errors {
		ch <- gauge(descPartErrors, 1, vin, e.Type)
	}

	if v.Odometer != nil {
		opt(ch, descOdometer, v.Odometer.MileageInKm, 1000, vin)
	}
	if v.AirConditioning != nil && v.AirConditioning.State != nil {
		enum(ch, descAirConditioner, vin, *v.AirConditioning.State, nil)
	}
	if v.Status != nil && v.Status.Overall != nil {
		if locked, ok := parseLocked(v.Status.Overall.DoorsLocked); ok {
			ch <- gauge(descDoorsLocked, b2f(locked), vin)
		}
	}
	if v.ChargingProfiles != nil && v.ChargingProfiles.CurrentVehiclePositionProfile != nil {
		opt(ch, descProfileTarget, v.ChargingProfiles.CurrentVehiclePositionProfile.TargetStateOfChargeInPercent, 0.01, vin)
	}

	chg := v.Charging
	if chg == nil {
		return
	}
	if chg.IsVehicleInSavedLocation != nil {
		ch <- gauge(descSavedLocation, b2f(*chg.IsVehicleInSavedLocation), vin)
	}
	if st := chg.Settings; st != nil {
		opt(ch, descTargetSoc, st.TargetStateOfChargeInPercent, 0.01, vin)
		opt(ch, descMaxCurrent, st.MaxChargeCurrentAcAmpere, 1, vin)
	}

	st := chg.Status
	if st == nil {
		return
	}
	opt(ch, descPower, st.ChargePowerInKw, 1000, vin)
	opt(ch, descRate, st.ChargingRateInKilometersPerHour, 1000, vin)
	opt(ch, descRemaining, st.RemainingTimeToFullyChargedInMinutes, 60, vin)
	if st.FullyChargedAt != nil {
		if t, err := time.Parse(time.RFC3339, *st.FullyChargedAt); err == nil {
			ch <- gauge(descFullyAt, unix(t), vin)
		}
	}
	if st.State != nil {
		enum(ch, descState, vin, *st.State, chargingStates)
	}
	if st.ChargeType != nil {
		enum(ch, descType, vin, *st.ChargeType, chargeTypes)
	}
	if st.PlugConnectionState != nil {
		switch *st.PlugConnectionState {
		case "CONNECTED":
			ch <- gauge(descPlugConnected, 1, vin)
		case "DISCONNECTED":
			ch <- gauge(descPlugConnected, 0, vin)
		}
	}
	if st.PlugLockState != nil {
		switch *st.PlugLockState {
		case "LOCKED":
			ch <- gauge(descPlugLocked, 1, vin)
		case "UNLOCKED":
			ch <- gauge(descPlugLocked, 0, vin)
		}
	}
	if b := st.Battery; b != nil {
		opt(ch, descSoc, b.StateOfChargeInPercent, 0.01, vin)
		opt(ch, descRange, b.RemainingCruisingRangeInMeters, 1, vin)
	}
}

// parseLocked reads doorsLocked as either a boolean or an enum string.
func parseLocked(raw json.RawMessage) (locked, ok bool) {
	if len(raw) == 0 {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false, false
	}
	switch strings.ToUpper(s) {
	case "YES", "TRUE", "LOCKED":
		return true, true
	case "NO", "FALSE", "UNLOCKED":
		return false, true
	}
	return false, false
}

func enum(ch chan<- prometheus.Metric, d *prometheus.Desc, vin, current string, known []string) {
	for _, k := range known {
		ch <- gauge(d, b2f(k == current), vin, k)
	}
	if !slices.Contains(known, current) {
		ch <- gauge(d, 1, vin, current)
	}
}

func opt(ch chan<- prometheus.Metric, d *prometheus.Desc, v *float64, scale float64, labels ...string) {
	if v != nil {
		ch <- gauge(d, *v*scale, labels...)
	}
}

func gauge(d *prometheus.Desc, v float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func unix(t time.Time) float64 {
	return float64(t.UnixNano()) / 1e9
}
