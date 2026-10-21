package api

// VehicleResponse is the body of GET /api/v1/vehicles/{vin}.
//
// The API omits parts it cannot deliver and lists them in Errors, and any
// field may be missing. Every optional value is therefore a pointer: nil
// means unknown, never zero.
type VehicleResponse struct {
	Vehicle Vehicle     `json:"vehicle"`
	Errors  []PartError `json:"errors"`
}

// PartError names a part of the vehicle data that could not be retrieved.
type PartError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type Vehicle struct {
	VIN               string             `json:"vin"`
	Name              string             `json:"name"`
	LicensePlate      string             `json:"licensePlate"`
	Status            *VehicleStatus     `json:"status"`
	Odometer          *Odometer          `json:"odometer"`
	AirConditioning   *AirConditioning   `json:"airConditioning"`
	AuxiliaryHeating  *AuxiliaryHeating  `json:"auxiliaryHeating"`
	ActiveVentilation *ActiveVentilation `json:"activeVentilation"`
	Charging          *Charging          `json:"charging"`
}

type VehicleStatus struct {
	Overall *OverallStatus `json:"overall"`
}

type OverallStatus struct {
	DoorsLocked *string `json:"doorsLocked"`
}

type Odometer struct {
	MileageInKm *float64 `json:"mileageInKm"`
}

type AirConditioning struct {
	State                               *string        `json:"state"`
	TargetTemperature                   *Temperature   `json:"targetTemperature"`
	EstimatedReachOfTargetTemperatureAt *string        `json:"estimatedReachOfTargetTemperatureAt"`
	AirConditioningWithoutExternalPower *bool          `json:"airConditioningWithoutExternalPower"`
	AirConditioningAtUnlock             *bool          `json:"airConditioningAtUnlock"`
	WindowHeating                       *WindowHeating `json:"windowHeating"`
}

type Temperature struct {
	Value *float64 `json:"value"`
	Unit  *string  `json:"unit"`
}

type WindowHeating struct {
	Enabled *bool   `json:"enabled"`
	Front   *string `json:"front"`
	Rear    *string `json:"rear"`
}

type AuxiliaryHeating struct {
	State                               *string      `json:"state"`
	StartMode                           *string      `json:"startMode"`
	DurationInSeconds                   *float64     `json:"durationInSeconds"`
	TargetTemperature                   *Temperature `json:"targetTemperature"`
	EstimatedReachOfTargetTemperatureAt *string      `json:"estimatedReachOfTargetTemperatureAt"`
}

type ActiveVentilation struct {
	State             *string  `json:"state"`
	DurationInSeconds *float64 `json:"durationInSeconds"`
}

type Charging struct {
	IsVehicleInSavedLocation *bool             `json:"isVehicleInSavedLocation"`
	Status                   *ChargingStatus   `json:"status"`
	Settings                 *ChargingSettings `json:"settings"`
}

type ChargingStatus struct {
	ChargingRateInKilometersPerHour      *float64 `json:"chargingRateInKilometersPerHour"`
	ChargePowerInKw                      *float64 `json:"chargePowerInKw"`
	RemainingTimeToFullyChargedInMinutes *float64 `json:"remainingTimeToFullyChargedInMinutes"`
	// FullyChargedAt is parsed by the collector so a format surprise drops
	// one metric instead of the whole response.
	FullyChargedAt      *string        `json:"fullyChargedAt"`
	State               *string        `json:"state"`
	ChargeType          *string        `json:"chargeType"`
	PlugConnectionState *string        `json:"plugConnectionState"`
	PlugLockState       *string        `json:"plugLockState"`
	Battery             *BatteryStatus `json:"battery"`
}

type BatteryStatus struct {
	RemainingCruisingRangeInMeters *float64 `json:"remainingCruisingRangeInMeters"`
	StateOfChargeInPercent         *float64 `json:"stateOfChargeInPercent"`
}

type ChargingSettings struct {
	TargetStateOfChargeInPercent *float64 `json:"targetStateOfChargeInPercent"`
	MaxChargeCurrentAcAmpere     *float64 `json:"maxChargeCurrentAcAmpere"`
}

// Problem is an RFC 9457 problem+json error body.
type Problem struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}
