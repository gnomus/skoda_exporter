package api

import "encoding/json"

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
	VIN              string            `json:"vin"`
	Name             string            `json:"name"`
	LicensePlate     string            `json:"licensePlate"`
	Status           *VehicleStatus    `json:"status"`
	Odometer         *Odometer         `json:"odometer"`
	AirConditioning  *AirConditioning  `json:"airConditioning"`
	Charging         *Charging         `json:"charging"`
	ChargingProfiles *ChargingProfiles `json:"chargingProfiles"`
}

type VehicleStatus struct {
	Overall *OverallStatus `json:"overall"`
}

type OverallStatus struct {
	// DoorsLocked is kept raw because the spec could not be checked yet:
	// it may be a boolean or an enum string.
	DoorsLocked json.RawMessage `json:"doorsLocked"`
}

type Odometer struct {
	MileageInKm *float64 `json:"mileageInKm"`
}

type AirConditioning struct {
	State *string `json:"state"`
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

type ChargingProfiles struct {
	CurrentVehiclePositionProfile *ChargingProfile `json:"currentVehiclePositionProfile"`
}

type ChargingProfile struct {
	TargetStateOfChargeInPercent *float64 `json:"targetStateOfChargeInPercent"`
}

// Problem is an RFC 9457 problem+json error body.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}
