package fleet

import (
	"time"
)

type Vehicle struct {
	VehicleID    string    `json:"vehicle_id"`
	Zone         string    `json:"zone"`
	Seats        int       `json:"seats"`
	Availability string    `json:"availability"`
	ObservedAt   time.Time `json:"observed_at"`
}

type FleetPage struct {
	Items      []Vehicle `json:"items"`
	NextCursor *string   `json:"next_cursor"`
	AsOf       time.Time `json:"as_of"`
}
type Reservation struct {
	RideID     string     `json:"ride_id"`
	VehicleID  *string    `json:"vehicle_id,omitempty"`
	State      string     `json:"state"`
	ReservedAt *time.Time `json:"reserved_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}
type ReserveRequest struct {
	RideID     string `json:"ride_id"`
	PickupZone string `json:"pickup_zone"`
	Passengers int    `json:"passengers"`
}

func effectiveAvailability(stored string, observed time.Time, maintenance bool, battery int, active bool, asOf time.Time) string {
	age := asOf.Sub(observed)
	if age < 0 || age > 30*time.Second {
		return "unknown"
	}
	if active {
		return "reserved"
	}
	if maintenance || battery < 20 || stored == "unavailable" {
		return "unavailable"
	}
	return "available"
}
