// Package collector turns the poller's cached responses into Prometheus
// metrics at scrape time.
package collector

import (
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

	acStates     = []string{"OFF", "COOLING", "HEATING", "HEATING_AUXILIARY", "VENTILATION", "COMPLETED", "UNKNOWN", "UNSUPPORTED"}
	auxStates    = []string{"OFF", "PREHEATING", "HEATING_AUXILIARY", "VENTILATION", "UNKNOWN", "UNSUPPORTED"}
	auxModes     = []string{"HEATING", "VENTILATION"}
	ventStates   = []string{"OFF", "PREHEATING", "VENTILATION", "UNKNOWN", "UNSUPPORTED"}
	windowStates = []string{"ON", "OFF", "UNKNOWN", "UNSUPPORTED"}
)

// Scale factors for scaledGauge, converting API units to Prometheus base units.
const (
	kmToMeters       = 1000
	kWToWatts        = 1000
	percentToRatio   = 0.01
	minutesToSeconds = 60
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
	descSavedLocation  = desc("charging_in_saved_location", "1 if the vehicle is at a saved charging location.")
	descAirConditioner = desc("air_conditioning_state", "Air conditioning state; 1 for the current state.", "state")
	descACTemp         = desc("air_conditioning_target_temperature_celsius", "Air conditioning target temperature.")
	descACReach        = desc("air_conditioning_target_reached_timestamp_seconds", "When the cabin is expected to reach the target temperature, as Unix time.")
	descACNoPower      = desc("air_conditioning_without_external_power", "1 if air conditioning is allowed without external power.")
	descACAtUnlock     = desc("air_conditioning_at_unlock", "1 if air conditioning starts at unlock.")
	descWindowEnabled  = desc("window_heating_enabled", "1 if window heating is enabled.")
	descWindowFront    = desc("window_heating_front_state", "Front window heating state; 1 for the current state.", "state")
	descWindowRear     = desc("window_heating_rear_state", "Rear window heating state; 1 for the current state.", "state")
	descAuxState       = desc("auxiliary_heating_state", "Auxiliary heating state; 1 for the current state.", "state")
	descAuxMode        = desc("auxiliary_heating_start_mode", "Auxiliary heating start mode; 1 for the current mode.", "mode")
	descAuxDuration    = desc("auxiliary_heating_duration_seconds", "Auxiliary heating run time when started.")
	descAuxTemp        = desc("auxiliary_heating_target_temperature_celsius", "Auxiliary heating target temperature.")
	descAuxReach       = desc("auxiliary_heating_target_reached_timestamp_seconds", "When the cabin is expected to reach the auxiliary heating target temperature, as Unix time.")
	descVentState      = desc("active_ventilation_state", "Active ventilation state; 1 for the current state.", "state")
	descVentDuration   = desc("active_ventilation_duration_seconds", "Active ventilation run time when started.")
	descDoorsLocked    = desc("doors_locked", "1 if the doors are locked.")

	descUp          = desc("up", "1 if the last API call for this VIN succeeded.")
	descLastSuccess = desc("last_success_timestamp_seconds", "Unix time of the last successful API call.")
	descPartErrors  = desc("api_part_errors", "Parts missing from the last successful response; 1 per error type.", "type")
	descRateLimit   = desc("api_ratelimit_remaining", "Remaining API calls in the current window, from RateLimit-Remaining.")
	// The key belongs to the account, not a VIN, so this has no vin label.
	descKeyExpiry = prometheus.NewDesc("skoda_api_key_expiry_timestamp_seconds", "Unix time when the API key expires, from X-API-Key-Expires-At.", nil, nil)
)

// Collector implements prometheus.Collector over a poller.Store. It is an
// unchecked collector: series come and go with the data.
type Collector struct {
	Store *poller.Store
	// StaleAfter drops vehicle metrics when the last success is older; 0
	// disables this.
	StaleAfter time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (c *Collector) Describe(chan<- *prometheus.Desc) {}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}

	s := c.Store.Snapshot()
	if !s.KeyExpiresAt.IsZero() {
		ch <- prometheus.MustNewConstMetric(descKeyExpiry, prometheus.GaugeValue, unix(s.KeyExpiresAt))
	}
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

func collectVehicle(ch chan<- prometheus.Metric, vin string, res *api.VehicleResponse) {
	v := res.Vehicle
	ch <- gauge(descInfo, 1, vin, v.Name, v.LicensePlate)

	seen := map[string]bool{}
	for _, e := range res.Errors {
		if seen[e.Type] {
			continue
		}
		seen[e.Type] = true
		ch <- gauge(descPartErrors, 1, vin, e.Type)
	}

	if v.Odometer != nil {
		scaledGauge(ch, descOdometer, vin, v.Odometer.MileageInKm, kmToMeters)
	}
	if ac := v.AirConditioning; ac != nil {
		enumGauge(ch, descAirConditioner, vin, ac.State, acStates)
		tempGauge(ch, descACTemp, vin, ac.TargetTemperature)
		timestampGauge(ch, descACReach, vin, ac.EstimatedReachOfTargetTemperatureAt)
		boolGauge(ch, descACNoPower, vin, ac.AirConditioningWithoutExternalPower)
		boolGauge(ch, descACAtUnlock, vin, ac.AirConditioningAtUnlock)
		if w := ac.WindowHeating; w != nil {
			boolGauge(ch, descWindowEnabled, vin, w.Enabled)
			enumGauge(ch, descWindowFront, vin, w.Front, windowStates)
			enumGauge(ch, descWindowRear, vin, w.Rear, windowStates)
		}
	}
	if a := v.AuxiliaryHeating; a != nil {
		enumGauge(ch, descAuxState, vin, a.State, auxStates)
		enumGauge(ch, descAuxMode, vin, a.StartMode, auxModes)
		scaledGauge(ch, descAuxDuration, vin, a.DurationInSeconds, 1)
		tempGauge(ch, descAuxTemp, vin, a.TargetTemperature)
		timestampGauge(ch, descAuxReach, vin, a.EstimatedReachOfTargetTemperatureAt)
	}
	if a := v.ActiveVentilation; a != nil {
		enumGauge(ch, descVentState, vin, a.State, ventStates)
		scaledGauge(ch, descVentDuration, vin, a.DurationInSeconds, 1)
	}
	if v.Status != nil && v.Status.Overall != nil {
		if d := v.Status.Overall.DoorsLocked; d != nil {
			if locked, ok := parseLocked(*d); ok {
				ch <- gauge(descDoorsLocked, b2f(locked), vin)
			}
		}
	}

	chg := v.Charging
	if chg == nil {
		return
	}
	boolGauge(ch, descSavedLocation, vin, chg.IsVehicleInSavedLocation)
	if st := chg.Settings; st != nil {
		scaledGauge(ch, descTargetSoc, vin, st.TargetStateOfChargeInPercent, percentToRatio)
		scaledGauge(ch, descMaxCurrent, vin, st.MaxChargeCurrentAcAmpere, 1)
	}

	st := chg.Status
	if st == nil {
		return
	}
	scaledGauge(ch, descPower, vin, st.ChargePowerInKw, kWToWatts)
	scaledGauge(ch, descRate, vin, st.ChargingRateInKilometersPerHour, kmToMeters)
	scaledGauge(ch, descRemaining, vin, st.RemainingTimeToFullyChargedInMinutes, minutesToSeconds)
	timestampGauge(ch, descFullyAt, vin, st.FullyChargedAt)
	enumGauge(ch, descState, vin, st.State, chargingStates)
	enumGauge(ch, descType, vin, st.ChargeType, chargeTypes)
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
		scaledGauge(ch, descSoc, vin, b.StateOfChargeInPercent, percentToRatio)
		scaledGauge(ch, descRange, vin, b.RemainingCruisingRangeInMeters, 1)
	}
}

// parseLocked maps the doorsLocked enum string to a bool.
func parseLocked(s string) (locked, ok bool) {
	switch strings.ToUpper(s) {
	case "YES", "TRUE", "LOCKED":
		return true, true
	case "NO", "FALSE", "UNLOCKED":
		return false, true
	}
	return false, false
}

// enumGauge emits one 0/1 series per known value, plus the current value if it is
// not a known one. A nil current emits nothing.
func enumGauge(ch chan<- prometheus.Metric, d *prometheus.Desc, vin string, current *string, known []string) {
	if current == nil {
		return
	}
	for _, k := range known {
		ch <- gauge(d, b2f(k == *current), vin, k)
	}
	if !slices.Contains(known, *current) {
		ch <- gauge(d, 1, vin, *current)
	}
}

// timestampGauge emits an RFC 3339 timestamp as Unix seconds; unparsable values are dropped.
func timestampGauge(ch chan<- prometheus.Metric, d *prometheus.Desc, vin string, s *string) {
	if s == nil {
		return
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		// The spec's examples omit seconds, e.g. 2026-06-12T07:30Z.
		t, err = time.Parse("2006-01-02T15:04Z07:00", *s)
	}
	if err == nil {
		ch <- gauge(d, unix(t), vin)
	}
}

// boolGauge emits a bool as 0 or 1; nil emits nothing.
func boolGauge(ch chan<- prometheus.Metric, d *prometheus.Desc, vin string, b *bool) {
	if b != nil {
		ch <- gauge(d, b2f(*b), vin)
	}
}

// tempGauge emits a temperature in Celsius; an unknown unit emits nothing.
func tempGauge(ch chan<- prometheus.Metric, d *prometheus.Desc, vin string, t *api.Temperature) {
	if t == nil || t.Value == nil || t.Unit == nil {
		return
	}
	switch *t.Unit {
	case "CELSIUS":
		ch <- gauge(d, *t.Value, vin)
	case "FAHRENHEIT":
		ch <- gauge(d, (*t.Value-32)*5/9, vin)
	}
}

// scaledGauge emits *v multiplied by scale; nil emits nothing.
func scaledGauge(ch chan<- prometheus.Metric, d *prometheus.Desc, vin string, v *float64, scale float64) {
	if v != nil {
		ch <- gauge(d, *v*scale, vin)
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
