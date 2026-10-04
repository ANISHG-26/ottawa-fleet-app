package ride

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrTripNotFound = errors.New("trip ride not found")
	ErrTripConflict = errors.New("trip command conflicts with current state")
	ErrTripPending  = errors.New("another trip effect is pending reconciliation")
)

type TripEvent struct {
	EventID string
	RideID  string
	Effect  string
	State   string
	Code    string
	Receipt *FleetEffectReceipt
}

type tripRepository interface {
	GetTrip(context.Context, string) (Trip, error)
	PrepareTripStart(context.Context, string, TripCommand) (TripEvent, Trip, error)
	PrepareTripCompletion(context.Context, string, TripCommand) (TripEvent, Trip, error)
	CommitTripEffect(context.Context, string, FleetEffectReceipt) (Trip, error)
	RejectTripEffect(context.Context, string, string) error
}

func (s *Store) GetTrip(ctx context.Context, rideID string) (Trip, error) {
	return loadTrip(ctx, s.db, rideID)
}

func (s *Store) PrepareTripStart(ctx context.Context, rideID string, command TripCommand) (TripEvent, Trip, error) {
	return s.prepareTripEffect(ctx, rideID, command, "trip_start")
}

func (s *Store) PrepareTripCompletion(ctx context.Context, rideID string, command TripCommand) (TripEvent, Trip, error) {
	return s.prepareTripEffect(ctx, rideID, command, "trip_complete")
}

func (s *Store) prepareTripEffect(ctx context.Context, rideID string, command TripCommand, effect string) (TripEvent, Trip, error) {
	if err := command.Validate(rideID); err != nil {
		return TripEvent{}, Trip{}, err
	}
	fingerprint, payload, err := payloadFingerprint(command)
	if err != nil {
		return TripEvent{}, Trip{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TripEvent{}, Trip{}, err
	}
	defer tx.Rollback()

	var event TripEvent
	var oldFingerprint string
	var oldReceipt sql.NullString
	var errorCode sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT event_id,ride_id,effect,state,error_code,payload_fingerprint,receipt
		FROM ride_trip_effect_events WHERE event_id=$1 FOR UPDATE`, command.EventID).
		Scan(&event.EventID, &event.RideID, &event.Effect, &event.State, &errorCode, &oldFingerprint, &oldReceipt)
	if err == nil {
		if errorCode.Valid {
			event.Code = errorCode.String
		}
		if event.RideID != rideID || event.Effect != effect || oldFingerprint != fingerprint {
			return TripEvent{}, Trip{}, ErrTripConflict
		}
		if oldReceipt.Valid && oldReceipt.String != "" {
			var receipt FleetEffectReceipt
			if json.Unmarshal([]byte(oldReceipt.String), &receipt) != nil {
				return TripEvent{}, Trip{}, errors.New("stored trip receipt is invalid")
			}
			event.Receipt = &receipt
		}
		trip, e := loadTrip(ctx, tx, rideID)
		if e != nil {
			return TripEvent{}, Trip{}, e
		}
		if e = tx.Commit(); e != nil {
			return TripEvent{}, Trip{}, e
		}
		return event, trip, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TripEvent{}, Trip{}, err
	}

	var assignmentState, assignedVehicle, pickup, dropoff string
	err = tx.QueryRowContext(ctx, `SELECT r.state,COALESCE(j.vehicle_id,''),r.pickup_zone,r.dropoff_zone
		FROM rides r JOIN assignment_jobs j USING(ride_id) WHERE r.ride_id=$1 FOR UPDATE OF r,j`, rideID).
		Scan(&assignmentState, &assignedVehicle, &pickup, &dropoff)
	if errors.Is(err, sql.ErrNoRows) {
		return TripEvent{}, Trip{}, ErrTripNotFound
	}
	if err != nil {
		return TripEvent{}, Trip{}, err
	}
	if assignmentState != "completed" || assignedVehicle == "" || assignedVehicle != command.VehicleID || command.ReservationID != rideID {
		return TripEvent{}, Trip{}, ErrTripConflict
	}
	if effect == "trip_start" && (pickup != "lansdowne" || dropoff != "centretown") {
		return TripEvent{}, Trip{}, ErrTripConflict
	}
	if effect == "trip_start" {
		_, err = tx.ExecContext(ctx, `INSERT INTO ride_trips(ride_id,trip_id,run_id,route_id,vehicle_id,reservation_id,
			start_zone,destination_zone,state,version,vehicle_version)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'not_started',1,$9) ON CONFLICT(ride_id) DO NOTHING`,
			rideID, command.TripID, command.RunID, command.RouteID, command.VehicleID, command.ReservationID,
			pickup, dropoff, command.ExpectedVehicleVersion)
		if err != nil {
			return TripEvent{}, Trip{}, err
		}
	}
	trip, err := loadTrip(ctx, tx, rideID)
	if err != nil {
		return TripEvent{}, Trip{}, err
	}
	if err = validateStoredTrip(trip, command, effect); err != nil {
		return TripEvent{}, Trip{}, err
	}
	var pending string
	err = tx.QueryRowContext(ctx, `SELECT event_id FROM ride_trip_effect_events WHERE ride_id=$1 AND state='pending'`, rideID).Scan(&pending)
	if err == nil {
		return TripEvent{}, Trip{}, ErrTripPending
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TripEvent{}, Trip{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ride_trip_effect_events(event_id,ride_id,trip_id,effect,normalized_payload,
		payload_fingerprint,state) VALUES($1,$2,$3,$4,$5::jsonb,$6,'pending')`,
		command.EventID, rideID, command.TripID, effect, string(payload), fingerprint)
	if err != nil {
		return TripEvent{}, Trip{}, err
	}
	if err = tx.Commit(); err != nil {
		return TripEvent{}, Trip{}, err
	}
	return TripEvent{EventID: command.EventID, RideID: rideID, Effect: effect, State: "pending"}, trip, nil
}

func validateStoredTrip(trip Trip, command TripCommand, effect string) error {
	if trip.TripID != command.TripID || trip.VehicleID != command.VehicleID ||
		trip.DestinationZone != "centretown" || trip.StartZone != "lansdowne" {
		return ErrTripConflict
	}
	if effect == "trip_start" {
		if trip.TripState != "not_started" || trip.Version != command.ExpectedTripVersion ||
			trip.Version != 1 || command.ExpectedVehicleVersion != trip.VehicleVersion {
			return ErrTripConflict
		}
		return nil
	}
	if effect == "trip_complete" {
		if trip.TripState != "in_progress" || trip.Version != command.ExpectedTripVersion ||
			trip.Version != 2 || command.ExpectedVehicleVersion != trip.VehicleVersion {
			return ErrTripConflict
		}
		return nil
	}
	return ErrTripConflict
}

func (s *Store) CommitTripEffect(ctx context.Context, eventID string, receipt FleetEffectReceipt) (Trip, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Trip{}, err
	}
	defer tx.Rollback()
	var rideID, effect, state string
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT ride_id,effect,state,normalized_payload
		FROM ride_trip_effect_events WHERE event_id=$1 FOR UPDATE`, eventID).
		Scan(&rideID, &effect, &state, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Trip{}, ErrTripNotFound
	}
	if err != nil {
		return Trip{}, err
	}
	if state == "accepted" {
		return loadTrip(ctx, tx, rideID)
	}
	if state != "pending" {
		return Trip{}, ErrTripConflict
	}
	var command TripCommand
	if err = json.Unmarshal(payload, &command); err != nil {
		return Trip{}, err
	}
	trip, err := loadTrip(ctx, tx, rideID)
	if err != nil {
		return Trip{}, err
	}
	if err = validateStoredTrip(trip, command, effect); err != nil {
		return Trip{}, err
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return Trip{}, err
	}
	if effect == "trip_start" {
		_, err = tx.ExecContext(ctx, `UPDATE ride_trips SET state='in_progress',version=version+1,
			vehicle_version=$2,started_at=clock_timestamp(),updated_at=clock_timestamp()
			WHERE ride_id=$1 AND state='not_started' AND version=$3`, rideID,
			receipt.AcceptedVehicleVersion, command.ExpectedTripVersion)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE ride_trips SET state='completed',version=version+1,
			vehicle_version=$2,completed_at=clock_timestamp(),updated_at=clock_timestamp()
			WHERE ride_id=$1 AND state='in_progress' AND version=$3`, rideID,
			receipt.AcceptedVehicleVersion, command.ExpectedTripVersion)
	}
	if err != nil {
		return Trip{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE ride_trip_effect_events SET state='accepted',receipt=$2::jsonb,updated_at=clock_timestamp()
		WHERE event_id=$1 AND state='pending'`, eventID, string(receiptJSON))
	if err != nil {
		return Trip{}, err
	}
	if err = tx.Commit(); err != nil {
		return Trip{}, err
	}
	return loadTrip(ctx, s.db, rideID)
}

func (s *Store) RejectTripEffect(ctx context.Context, eventID, code string) error {
	if code == "" {
		code = "fleet_conflict"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE ride_trip_effect_events SET state='rejected',error_code=$2,updated_at=clock_timestamp()
		WHERE event_id=$1 AND state='pending'`, eventID, code)
	return err
}

type tripQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadTrip(ctx context.Context, q tripQuery, rideID string) (Trip, error) {
	var out Trip
	var tripID, vehicleID, startZone, destinationZone, tripState sql.NullString
	var startedAt, completedAt sql.NullTime
	var version, vehicleVersion sql.NullInt64
	var rideUpdatedAt time.Time
	var tripUpdatedAt sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT r.ride_id,r.state,r.updated_at,
		t.trip_id,t.vehicle_id,t.start_zone,t.destination_zone,t.state,t.version,t.vehicle_version,t.started_at,t.completed_at,t.updated_at
		FROM rides r LEFT JOIN ride_trips t USING(ride_id) WHERE r.ride_id=$1`, rideID).
		Scan(&out.RideID, &out.AssignmentState, &rideUpdatedAt, &tripID, &vehicleID, &startZone,
			&destinationZone, &tripState, &version, &vehicleVersion, &startedAt, &completedAt, &tripUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Trip{}, ErrTripNotFound
	}
	if err != nil {
		return Trip{}, err
	}
	out.TripState = "not_started"
	out.Version = 1
	out.VehicleVersion = 0
	out.UpdatedAt = rideUpdatedAt.UTC()
	if tripID.Valid {
		out.TripID = tripID.String
		out.VehicleID = vehicleID.String
		out.StartZone = startZone.String
		out.DestinationZone = destinationZone.String
		out.TripState = tripState.String
		out.Version = version.Int64
		out.VehicleVersion = vehicleVersion.Int64
		if tripUpdatedAt.Valid {
			out.UpdatedAt = tripUpdatedAt.Time.UTC()
		}
	}
	if startedAt.Valid {
		t := startedAt.Time.UTC()
		out.StartedAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time.UTC()
		out.CompletedAt = &t
	}
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out, nil
}
