package ride

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

const tripRouteID = "lansdowne-centretown-v1"

var tripIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

type Trip struct {
	RideID          string     `json:"ride_id"`
	AssignmentState string     `json:"assignment_state"`
	TripState       string     `json:"trip_state"`
	TripID          string     `json:"trip_id,omitempty"`
	VehicleID       string     `json:"vehicle_id,omitempty"`
	StartZone       string     `json:"start_zone,omitempty"`
	DestinationZone string     `json:"destination_zone,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
	Version         int64      `json:"version"`
	VehicleVersion  int64      `json:"-"`
}

type TripCommand struct {
	EventID                string `json:"event_id"`
	TripID                 string `json:"trip_id"`
	VehicleID              string `json:"vehicle_id"`
	ReservationID          string `json:"reservation_id"`
	RunID                  string `json:"run_id"`
	RouteID                string `json:"route_id"`
	ExpectedTripVersion    int64  `json:"expected_trip_version"`
	ExpectedVehicleVersion int64  `json:"expected_vehicle_version"`
}

func (c TripCommand) Validate(rideID string) error {
	for name, id := range map[string]string{
		"ride_id": rideID, "event_id": c.EventID, "trip_id": c.TripID,
		"vehicle_id": c.VehicleID, "reservation_id": c.ReservationID, "run_id": c.RunID,
	} {
		if !tripIDPattern.MatchString(id) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if c.RouteID != tripRouteID {
		return errors.New("route_id is unsupported")
	}
	if c.ExpectedTripVersion < 1 || c.ExpectedVehicleVersion < 1 {
		return errors.New("expected versions must be positive")
	}
	if c.ReservationID != rideID {
		return errors.New("reservation_id does not match the ride reservation")
	}
	return nil
}

type TripPosition struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type FleetStartTripCommand struct {
	EventID                string `json:"event_id"`
	RunID                  string `json:"run_id"`
	RideID                 string `json:"ride_id"`
	TripID                 string `json:"trip_id"`
	VehicleID              string `json:"vehicle_id"`
	ReservationID          string `json:"reservation_id"`
	RouteID                string `json:"route_id"`
	ExpectedVehicleVersion int64  `json:"expected_vehicle_version"`
}

type FleetCompleteTripCommand struct {
	EventID                string       `json:"event_id"`
	RunID                  string       `json:"run_id"`
	RideID                 string       `json:"ride_id"`
	TripID                 string       `json:"trip_id"`
	VehicleID              string       `json:"vehicle_id"`
	ReservationID          string       `json:"reservation_id"`
	RouteID                string       `json:"route_id"`
	ExpectedVehicleVersion int64        `json:"expected_vehicle_version"`
	DestinationZone        string       `json:"destination_zone"`
	DestinationPosition    TripPosition `json:"destination_position"`
}

type FleetEffectReceipt struct {
	EventID                string    `json:"event_id"`
	Effect                 string    `json:"effect"`
	RunID                  string    `json:"run_id"`
	RideID                 string    `json:"ride_id"`
	TripID                 string    `json:"trip_id"`
	VehicleID              string    `json:"vehicle_id"`
	ReservationID          string    `json:"reservation_id"`
	PayloadFingerprint     string    `json:"payload_fingerprint"`
	AcceptedVehicleVersion int64     `json:"accepted_vehicle_version"`
	StoredAt               time.Time `json:"stored_at"`
	Result                 string    `json:"result"`
}

type TripEffectResult struct {
	Status  int
	Code    string
	Receipt FleetEffectReceipt
}

func payloadFingerprint(v any) (string, []byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), b, nil
}
