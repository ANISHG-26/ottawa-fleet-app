package fleet

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
)

const simulationRouteID = "lansdowne-centretown-v1"

type SimulationPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
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

type fleetTripCommand struct {
	EventID                string           `json:"event_id"`
	RunID                  string           `json:"run_id"`
	RideID                 string           `json:"ride_id"`
	TripID                 string           `json:"trip_id"`
	VehicleID              string           `json:"vehicle_id"`
	ReservationID          string           `json:"reservation_id"`
	RouteID                string           `json:"route_id"`
	ExpectedVehicleVersion int64            `json:"expected_vehicle_version"`
	DestinationZone        string           `json:"destination_zone,omitempty"`
	DestinationPosition    *SimulationPoint `json:"destination_position,omitempty"`
}

type fleetPositionCommand struct {
	EventID                string          `json:"event_id"`
	RunID                  string          `json:"run_id"`
	TripID                 string          `json:"trip_id"`
	RouteID                string          `json:"route_id"`
	ExpectedVehicleVersion int64           `json:"expected_vehicle_version"`
	Position               SimulationPoint `json:"position"`
	RouteSegment           int             `json:"route_segment"`
}

type FleetPosition struct {
	VehicleID        string           `json:"vehicle_id"`
	Position         SimulationPoint  `json:"position"`
	ObservedAt       time.Time        `json:"observed_at"`
	AsOf             time.Time        `json:"as_of"`
	Freshness        string           `json:"freshness"`
	OperationalState string           `json:"operational_state"`
	VehicleVersion   int64            `json:"vehicle_version"`
	RouteID          string           `json:"route_id,omitempty"`
	RouteVersion     int              `json:"route_version,omitempty"`
	RouteSegment     *int             `json:"route_segment,omitempty"`
	SegmentStart     *SimulationPoint `json:"segment_start,omitempty"`
	SegmentEnd       *SimulationPoint `json:"segment_end,omitempty"`
	SegmentStartedAt *time.Time       `json:"segment_started_at,omitempty"`
	SegmentEndsAt    *time.Time       `json:"segment_ends_at,omitempty"`
}

func (h *Handler) routeSimulation(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/v2/fleet/events/") {
		if r.Method != http.MethodGet {
			httpapi.WriteError(w, r, 404, "not_found", "resource not found")
			return true
		}
		h.getFleetEffectReceipt(w, r, strings.TrimPrefix(r.URL.Path, "/v2/fleet/events/"))
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/v2/fleet/vehicles/") {
		rest := strings.TrimPrefix(r.URL.Path, "/v2/fleet/vehicles/")
		parts := strings.Split(rest, "/")
		if len(parts) == 2 && parts[1] == "position" {
			if r.Method == http.MethodGet {
				h.getSimulationPosition(w, r, parts[0])
			} else if r.Method == http.MethodPut {
				h.applyFleetPositionObservation(w, r, parts[0])
			} else {
				httpapi.WriteError(w, r, 404, "not_found", "resource not found")
			}
			return true
		}
		if len(parts) == 3 && parts[1] == "trips" && r.Method == http.MethodPost {
			switch parts[2] {
			case "start":
				h.applyFleetTripEffect(w, r, parts[0], "trip_start")
			case "completion":
				h.applyFleetTripEffect(w, r, parts[0], "trip_complete")
			default:
				httpapi.WriteError(w, r, 404, "not_found", "resource not found")
			}
			return true
		}
	}
	return false
}

func effectFingerprint(command any) (string, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (h *Handler) applyFleetPositionObservation(w http.ResponseWriter, r *http.Request, vehicleID string) {
	if !entityID.MatchString(vehicleID) {
		httpapi.WriteError(w, r, 400, "invalid_request", "vehicle ID is invalid")
		return
	}
	var cmd fleetPositionCommand
	if p := httpapi.DecodeStrict(w, r, &cmd); p != nil {
		writeProblem(w, r, p)
		return
	}
	if !entityID.MatchString(cmd.EventID) || !entityID.MatchString(cmd.RunID) || !entityID.MatchString(cmd.TripID) ||
		cmd.RouteID != simulationRouteID || cmd.ExpectedVehicleVersion < 1 || cmd.RouteSegment < 0 || cmd.RouteSegment > 19 ||
		cmd.Position != routePoint(cmd.RouteSegment) {
		httpapi.WriteError(w, r, 400, "invalid_request", "position event identity, route segment or catalog position is invalid")
		return
	}
	fingerprint, err := effectFingerprint(cmd)
	if err != nil {
		httpapi.WriteError(w, r, 500, "internal_error", "could not fingerprint position event")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	defer tx.Rollback()
	if err := lockSimulationKeys(r, tx, cmd.EventID, cmd.TripID); err != nil {
		dbUnavailable(w, r)
		return
	}
	if replayFleetEffect(w, r, tx, cmd.EventID, fingerprint) {
		return
	}
	var version int64
	var opState, tripID, runID, rideID string
	var tripStarted time.Time
	err = tx.QueryRowContext(r.Context(), `SELECT vehicle_version,operational_state,COALESCE(active_trip_id,''),COALESCE(active_run_id,''),COALESCE(active_ride_id,''),trip_started_at FROM fleet_vehicles WHERE vehicle_id=$1 FOR UPDATE`, vehicleID).Scan(&version, &opState, &tripID, &runID, &rideID, &tripStarted)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 404, "not_found", "vehicle not found")
		return
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	if replayFleetEffect(w, r, tx, cmd.EventID, fingerprint) {
		return
	}
	if version != cmd.ExpectedVehicleVersion {
		httpapi.WriteError(w, r, 409, "version_conflict", "vehicle version changed")
		return
	}
	if opState != "on_trip" || tripID != cmd.TripID || runID != cmd.RunID {
		httpapi.WriteError(w, r, 409, "version_conflict", "position event does not match active trip")
		return
	}
	now := h.clock().UTC()
	elapsed := now.Sub(tripStarted)
	currentSegment := int(math.Floor(elapsed.Seconds() / .4))
	if currentSegment < 0 {
		currentSegment = 0
	}
	if currentSegment > 19 {
		currentSegment = 19
	}
	if cmd.RouteSegment != currentSegment {
		httpapi.WriteError(w, r, 409, "version_conflict", "position event is outside the server-clock route segment")
		return
	}
	start, end := routePoint(cmd.RouteSegment), routePoint(cmd.RouteSegment+1)
	begin := tripStarted.Add(time.Duration(cmd.RouteSegment) * 400 * time.Millisecond)
	finish := begin.Add(400 * time.Millisecond)
	var newVersion int64
	err = tx.QueryRowContext(r.Context(), `UPDATE fleet_vehicles SET position_latitude=$2,position_longitude=$3,route_segment=$4,segment_start_latitude=$5,segment_start_longitude=$6,segment_end_latitude=$7,segment_end_longitude=$8,segment_started_at=$9,segment_ends_at=$10,observed_at=$11,vehicle_version=vehicle_version+1 WHERE vehicle_id=$1 RETURNING vehicle_version`, vehicleID, cmd.Position.Latitude, cmd.Position.Longitude, cmd.RouteSegment, start.Latitude, start.Longitude, end.Latitude, end.Longitude, begin, finish, now).Scan(&newVersion)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	receipt := FleetEffectReceipt{EventID: cmd.EventID, Effect: "position_observation", RunID: runID, RideID: rideID, TripID: tripID, VehicleID: vehicleID, ReservationID: rideID, PayloadFingerprint: fingerprint, AcceptedVehicleVersion: newVersion, StoredAt: now, Result: "applied"}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		httpapi.WriteError(w, r, 500, "internal_error", "could not encode receipt")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO fleet_simulation_effects(event_id,effect,run_id,ride_id,trip_id,vehicle_id,reservation_id,payload_fingerprint,accepted_vehicle_version,stored_at,receipt) VALUES($1,'position_observation',$2,$3,$4,$5,$3,$6,$7,$8,$9::jsonb)`, cmd.EventID, runID, rideID, tripID, vehicleID, fingerprint, newVersion, now, encoded)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	if err := tx.Commit(); err != nil {
		dbUnavailable(w, r)
		return
	}
	httpapi.WriteJSON(w, 200, receipt)
}

func (h *Handler) getFleetEffectReceipt(w http.ResponseWriter, r *http.Request, eventID string) {
	if !entityID.MatchString(eventID) {
		httpapi.WriteError(w, r, 400, "invalid_request", "event ID is invalid")
		return
	}
	var body []byte
	err := h.db.QueryRowContext(r.Context(), `SELECT receipt FROM fleet_simulation_effects WHERE event_id=$1`, eventID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 404, "not_found", "effect receipt not found")
		return
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

func (h *Handler) applyFleetTripEffect(w http.ResponseWriter, r *http.Request, vehicleID, effect string) {
	if !entityID.MatchString(vehicleID) {
		httpapi.WriteError(w, r, 400, "invalid_request", "vehicle ID is invalid")
		return
	}
	var cmd fleetTripCommand
	if p := httpapi.DecodeStrict(w, r, &cmd); p != nil {
		writeProblem(w, r, p)
		return
	}
	if !entityID.MatchString(cmd.EventID) || !entityID.MatchString(cmd.RunID) ||
		!entityID.MatchString(cmd.RideID) || !entityID.MatchString(cmd.TripID) || !entityID.MatchString(cmd.VehicleID) ||
		!entityID.MatchString(cmd.ReservationID) || cmd.VehicleID != vehicleID || cmd.ReservationID != cmd.RideID ||
		cmd.RouteID != simulationRouteID || cmd.ExpectedVehicleVersion < 1 {
		httpapi.WriteError(w, r, 400, "invalid_request", "trip effect identity, route or expected version is invalid")
		return
	}
	if effect == "trip_start" && (cmd.DestinationZone != "" || cmd.DestinationPosition != nil) {
		httpapi.WriteError(w, r, 400, "invalid_request", "trip start cannot include completion destination fields")
		return
	}
	if effect == "trip_complete" && (cmd.DestinationZone != "centretown" || cmd.DestinationPosition == nil || *cmd.DestinationPosition != routePoint(20)) {
		httpapi.WriteError(w, r, 400, "invalid_request", "completion destination does not match the route catalog")
		return
	}
	fingerprint, err := effectFingerprint(cmd)
	if err != nil {
		httpapi.WriteError(w, r, 500, "internal_error", "could not fingerprint effect")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	defer tx.Rollback()
	if err := lockSimulationKeys(r, tx, cmd.EventID, cmd.TripID); err != nil {
		dbUnavailable(w, r)
		return
	}
	if replayFleetEffect(w, r, tx, cmd.EventID, fingerprint) {
		return
	}
	var version int64
	var opState, activeTrip, activeRide, activeRun, vehicleZone string
	err = tx.QueryRowContext(r.Context(), `SELECT vehicle_version,operational_state,COALESCE(active_trip_id,''),COALESCE(active_ride_id,''),COALESCE(active_run_id,''),zone FROM fleet_vehicles WHERE vehicle_id=$1 FOR UPDATE`, vehicleID).Scan(&version, &opState, &activeTrip, &activeRide, &activeRun, &vehicleZone)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 404, "not_found", "vehicle not found")
		return
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	// Serialize same-event races on the vehicle, then check the durable receipt again
	// before comparing versions so a duplicate still replays after version advancement.
	if replayFleetEffect(w, r, tx, cmd.EventID, fingerprint) {
		return
	}
	if version != cmd.ExpectedVehicleVersion {
		httpapi.WriteError(w, r, 409, "version_conflict", "vehicle version changed")
		return
	}
	var reservationVehicle sql.NullString
	var reservationState string
	var reservationPickup sql.NullString
	var reservationUpdated time.Time
	err = tx.QueryRowContext(r.Context(), `SELECT vehicle_id,state,updated_at,pickup_zone FROM fleet_reservations WHERE ride_id=$1 FOR UPDATE`, cmd.RideID).Scan(&reservationVehicle, &reservationState, &reservationUpdated, &reservationPickup)
	if errors.Is(err, sql.ErrNoRows) || !reservationVehicle.Valid || reservationVehicle.String != vehicleID || reservationState != "reserved" {
		httpapi.WriteError(w, r, 409, "version_conflict", "ride reservation does not match the active trip")
		return
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	now := h.clock().UTC()
	var resultVersion int64
	var receipt FleetEffectReceipt
	if effect == "trip_start" {
		if vehicleZone != "lansdowne" || !reservationPickup.Valid || reservationPickup.String != "lansdowne" {
			httpapi.WriteError(w, r, 409, "version_conflict", "vehicle and reservation must be at the route origin")
			return
		}
		if opState == "on_trip" || opState == "out_of_service" || activeTrip != "" {
			httpapi.WriteError(w, r, 409, "version_conflict", "vehicle already has an active trip")
			return
		}
		start, end := routePoint(0), routePoint(1)
		segmentEnd := now.Add(400 * time.Millisecond)
		err = tx.QueryRowContext(r.Context(), `UPDATE fleet_vehicles SET operational_state='on_trip',active_trip_id=$2,active_ride_id=$3,active_run_id=$4,route_id=$5,route_version=1,route_segment=0,trip_started_at=$6,position_latitude=$7,position_longitude=$8,segment_start_latitude=$7,segment_start_longitude=$8,segment_end_latitude=$9,segment_end_longitude=$10,segment_started_at=$6,segment_ends_at=$11,observed_at=GREATEST($6,observed_at),vehicle_version=vehicle_version+1 WHERE vehicle_id=$1 RETURNING vehicle_version`, vehicleID, cmd.TripID, cmd.RideID, cmd.RunID, cmd.RouteID, now, start.Latitude, start.Longitude, end.Latitude, end.Longitude, segmentEnd).Scan(&resultVersion)
	} else {
		if opState != "on_trip" || activeTrip != cmd.TripID || activeRide != cmd.RideID || activeRun != cmd.RunID {
			httpapi.WriteError(w, r, 409, "version_conflict", "active trip identity does not match")
			return
		}
		var tripStarted time.Time
		if err = tx.QueryRowContext(r.Context(), `SELECT trip_started_at FROM fleet_vehicles WHERE vehicle_id=$1`, vehicleID).Scan(&tripStarted); err != nil {
			dbUnavailable(w, r)
			return
		}
		if now.Before(tripStarted.Add(8 * time.Second)) {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "synthetic trip has not reached its eight-second destination; retry this event")
			return
		}
		end := routePoint(20)
		err = tx.QueryRowContext(r.Context(), `UPDATE fleet_vehicles SET zone=$2,operational_state='available',active_trip_id=NULL,active_ride_id=NULL,active_run_id=NULL,route_id=NULL,route_version=NULL,route_segment=NULL,trip_started_at=NULL,position_latitude=$3,position_longitude=$4,segment_start_latitude=NULL,segment_start_longitude=NULL,segment_end_latitude=NULL,segment_end_longitude=NULL,segment_started_at=NULL,segment_ends_at=NULL,observed_at=GREATEST($5,observed_at),vehicle_version=vehicle_version+1 WHERE vehicle_id=$1 RETURNING vehicle_version`, vehicleID, cmd.DestinationZone, end.Latitude, end.Longitude, now).Scan(&resultVersion)
		if err == nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE fleet_reservations SET state='released',updated_at=GREATEST($2,updated_at) WHERE ride_id=$1 AND state='reserved'`, cmd.RideID, now)
		}
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	receipt = FleetEffectReceipt{EventID: cmd.EventID, Effect: effect, RunID: cmd.RunID, RideID: cmd.RideID, TripID: cmd.TripID,
		VehicleID: vehicleID, ReservationID: cmd.ReservationID, PayloadFingerprint: fingerprint,
		AcceptedVehicleVersion: resultVersion, StoredAt: now, Result: "applied"}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		httpapi.WriteError(w, r, 500, "internal_error", "could not encode receipt")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO fleet_simulation_effects(event_id,effect,run_id,ride_id,trip_id,vehicle_id,reservation_id,payload_fingerprint,accepted_vehicle_version,stored_at,receipt) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)`, cmd.EventID, effect, cmd.RunID, cmd.RideID, cmd.TripID, vehicleID, cmd.ReservationID, fingerprint, resultVersion, now, encoded)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	if err := tx.Commit(); err != nil {
		dbUnavailable(w, r)
		return
	}
	httpapi.WriteJSON(w, 200, receipt)
}

func lockSimulationKeys(r *http.Request, tx *sql.Tx, eventID, tripID string) error {
	keys := []string{"fleet-event:" + eventID, "fleet-trip:" + tripID}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
			return err
		}
	}
	return nil
}

func replayFleetEffect(w http.ResponseWriter, r *http.Request, tx *sql.Tx, eventID, fingerprint string) bool {
	var oldFingerprint string
	var receipt []byte
	err := tx.QueryRowContext(r.Context(), `SELECT payload_fingerprint,receipt FROM fleet_simulation_effects WHERE event_id=$1`, eventID).Scan(&oldFingerprint, &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		dbUnavailable(w, r)
		return true
	}
	if oldFingerprint != fingerprint {
		httpapi.WriteError(w, r, 409, "idempotency_conflict", "event ID was used with a different payload")
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(receipt)
	return true
}

func routePoint(i int) SimulationPoint {
	if i < 0 {
		i = 0
	}
	if i > 20 {
		i = 20
	}
	return SimulationPoint{Latitude: math.Round((45.399+0.021*float64(i)/20)*1e6) / 1e6,
		Longitude: math.Round((-75.684-0.009*float64(i)/20)*1e6) / 1e6}
}

func (h *Handler) getSimulationPosition(w http.ResponseWriter, r *http.Request, vehicleID string) {
	if !entityID.MatchString(vehicleID) {
		httpapi.WriteError(w, r, 400, "invalid_request", "vehicle ID is invalid")
		return
	}
	var out FleetPosition
	var opState, zone string
	var availability string
	var maintenance bool
	var battery int
	var active bool
	var lat, lon sql.NullFloat64
	var activeTrip sql.NullString
	var tripStarted sql.NullTime
	err := h.db.QueryRowContext(r.Context(), `SELECT v.vehicle_id,v.zone,v.operational_state,v.availability,v.maintenance,v.battery_percent,v.vehicle_version,v.observed_at,v.position_latitude,v.position_longitude,v.active_trip_id,v.trip_started_at,EXISTS(SELECT 1 FROM fleet_reservations r WHERE r.vehicle_id=v.vehicle_id AND r.state='reserved') FROM fleet_vehicles v WHERE v.vehicle_id=$1`, vehicleID).Scan(&out.VehicleID, &zone, &opState, &availability, &maintenance, &battery, &out.VehicleVersion, &out.ObservedAt, &lat, &lon, &activeTrip, &tripStarted, &active)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 404, "not_found", "vehicle not found")
		return
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	out.AsOf = h.clock().UTC()
	out.OperationalState = opState
	if opState != "on_trip" && (maintenance || battery < 20 || availability == "unavailable") {
		out.OperationalState = "out_of_service"
	} else if opState == "reserved" && !active {
		out.OperationalState = "available"
	} else if opState == "available" && active {
		out.OperationalState = "reserved"
	}
	if lat.Valid && lon.Valid {
		out.Position = SimulationPoint{Latitude: lat.Float64, Longitude: lon.Float64}
	} else {
		out.Position = zonePoint(zone)
	}
	out.Freshness = "fresh"
	age := out.AsOf.Sub(out.ObservedAt)
	if age < 0 {
		out.Freshness = "future"
	} else if age > 30*time.Second {
		out.Freshness = "stale"
	}
	out.ObservedAt = out.ObservedAt.UTC()
	if opState == "on_trip" && activeTrip.Valid && tripStarted.Valid {
		elapsed := out.AsOf.Sub(tripStarted.Time)
		segment := int(math.Floor(elapsed.Seconds() / 0.4))
		if segment < 0 {
			segment = 0
		}
		if segment > 19 {
			segment = 19
		}
		start, end := routePoint(segment), routePoint(segment+1)
		begin := tripStarted.Time.Add(time.Duration(segment) * 400 * time.Millisecond)
		finish := begin.Add(400 * time.Millisecond)
		fraction := out.AsOf.Sub(begin).Seconds() / 0.4
		if fraction < 0 {
			fraction = 0
		}
		if fraction > 1 {
			fraction = 1
		}
		out.Position = SimulationPoint{Latitude: start.Latitude + (end.Latitude-start.Latitude)*fraction,
			Longitude: start.Longitude + (end.Longitude-start.Longitude)*fraction}
		out.RouteID, out.RouteVersion = simulationRouteID, 1
		out.RouteSegment = &segment
		out.SegmentStart, out.SegmentEnd = &start, &end
		beginUTC, finishUTC := begin.UTC(), finish.UTC()
		out.SegmentStartedAt, out.SegmentEndsAt = &beginUTC, &finishUTC
	}
	httpapi.WriteJSON(w, 200, out)
}

func zonePoint(zone string) SimulationPoint {
	switch zone {
	case "lansdowne":
		return routePoint(0)
	case "centretown":
		return routePoint(20)
	case "glebe":
		return SimulationPoint{Latitude: 45.397, Longitude: -75.683}
	default:
		return SimulationPoint{Latitude: 45.429, Longitude: -75.689}
	}
}
