package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
)

func TestSimulationFleetEffectsPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL simulation test skipped")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := filepath.Clean(filepath.Join("..", "..", "..", "db"))
	if err := database.ApplyMigrations(ctx, db, root); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	vehicleID := "vehicle-simulation-" + suffix
	rideA, rideB := "ride-simulation-a-"+suffix, "ride-simulation-b-"+suffix
	_, _ = db.ExecContext(ctx, `DELETE FROM fleet_simulation_effects WHERE vehicle_id=$1`, vehicleID)
	_, _ = db.ExecContext(ctx, `DELETE FROM fleet_reservations WHERE ride_id IN ($1,$2)`, rideA, rideB)
	_, _ = db.ExecContext(ctx, `DELETE FROM fleet_vehicles WHERE vehicle_id=$1`, vehicleID)
	defer func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM fleet_simulation_effects WHERE vehicle_id=$1`, vehicleID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM fleet_reservations WHERE ride_id IN ($1,$2)`, rideA, rideB)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM fleet_vehicles WHERE vehicle_id=$1`, vehicleID)
	}()
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_vehicles(vehicle_id,zone,seats,availability,observed_at,vehicle_version,operational_state) VALUES($1,'lansdowne',4,'available',$2,1,'available')`, vehicleID, clock); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_reservations(ride_id,vehicle_id,state,reserved_at,updated_at,pickup_zone,passengers) VALUES($1,$3,'reserved',$2,$2,'lansdowne',1),($4,NULL,'released',NULL,$2,NULL,NULL)`, rideA, clock, vehicleID, rideB); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, func() time.Time { return clock }, FaultConfig{})
	call := func(method, path string, value any) *httptest.ResponseRecorder {
		var body bytes.Buffer
		if value != nil {
			_ = json.NewEncoder(&body).Encode(value)
		}
		r := httptest.NewRequest(method, path, &body)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	reservedPosition := call(http.MethodGet, "/v2/fleet/vehicles/"+vehicleID+"/position", nil)
	var reservedState FleetPosition
	if reservedPosition.Code != 200 || json.Unmarshal(reservedPosition.Body.Bytes(), &reservedState) != nil || reservedState.OperationalState != "reserved" {
		t.Fatalf("held vehicle position status=%d state=%s body=%s", reservedPosition.Code, reservedState.OperationalState, reservedPosition.Body.String())
	}
	startA := fleetTripCommand{EventID: "event-sim-start-a-" + suffix, RunID: "run-sim-test-" + suffix, RideID: rideA, TripID: "trip-sim-a-" + suffix, VehicleID: vehicleID, ReservationID: rideA, RouteID: simulationRouteID, ExpectedVehicleVersion: 1}
	started := call(http.MethodPost, "/v2/fleet/vehicles/"+vehicleID+"/trips/start", startA)
	if started.Code != 200 {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
	}
	var startReceipt FleetEffectReceipt
	if err := json.Unmarshal(started.Body.Bytes(), &startReceipt); err != nil {
		t.Fatal(err)
	}
	if startReceipt.AcceptedVehicleVersion != 2 {
		t.Fatalf("start version=%d", startReceipt.AcceptedVehicleVersion)
	}
	if released := call(http.MethodDelete, "/v1/reservations/"+rideA, nil); released.Code != 409 {
		t.Fatalf("active-trip reservation release status=%d body=%s", released.Code, released.Body.String())
	}
	destination := routePoint(20)
	completeA := startA
	completeA.EventID, completeA.ExpectedVehicleVersion = "event-sim-complete-a-"+suffix, 2
	completeA.DestinationZone, completeA.DestinationPosition = "centretown", &destination
	if early := call(http.MethodPost, "/v2/fleet/vehicles/"+vehicleID+"/trips/completion", completeA); early.Code != 503 {
		t.Fatalf("early completion should be retryable status=%d body=%s", early.Code, early.Body.String())
	}
	clock = clock.Add(8 * time.Second)
	positionResponse := call(http.MethodGet, "/v2/fleet/vehicles/"+vehicleID+"/position", nil)
	if positionResponse.Code != 200 {
		t.Fatalf("position status=%d body=%s", positionResponse.Code, positionResponse.Body.String())
	}
	var position FleetPosition
	if err := json.Unmarshal(positionResponse.Body.Bytes(), &position); err != nil {
		t.Fatal(err)
	}
	if position.OperationalState != "on_trip" || position.RouteSegment == nil || *position.RouteSegment != 19 || position.Position != destination {
		t.Fatalf("eight-second route endpoint position=%+v", position)
	}
	observation := fleetPositionCommand{EventID: "event-sim-position-" + suffix, RunID: startA.RunID, TripID: startA.TripID,
		RouteID: simulationRouteID, ExpectedVehicleVersion: 2, Position: routePoint(19), RouteSegment: 19}
	observed := call(http.MethodPut, "/v2/fleet/vehicles/"+vehicleID+"/position", observation)
	if observed.Code != 200 {
		t.Fatalf("position observation status=%d body=%s", observed.Code, observed.Body.String())
	}
	var positionReceipt FleetEffectReceipt
	if err := json.Unmarshal(observed.Body.Bytes(), &positionReceipt); err != nil || positionReceipt.AcceptedVehicleVersion != 3 || positionReceipt.Effect != "position_observation" {
		t.Fatalf("position receipt=%+v err=%v", positionReceipt, err)
	}
	// Replay after advancement still returns the same receipt; unseen stale writes fail CAS.
	positionReplay := call(http.MethodPut, "/v2/fleet/vehicles/"+vehicleID+"/position", observation)
	var replayPositionReceipt FleetEffectReceipt
	if positionReplay.Code != 200 || json.Unmarshal(positionReplay.Body.Bytes(), &replayPositionReceipt) != nil || replayPositionReceipt != positionReceipt {
		t.Fatalf("position replay status=%d body=%s", positionReplay.Code, positionReplay.Body.String())
	}
	lateObservation := observation
	lateObservation.EventID = "event-sim-position-late-" + suffix
	if late := call(http.MethodPut, "/v2/fleet/vehicles/"+vehicleID+"/position", lateObservation); late.Code != 409 {
		t.Fatalf("late unseen position status=%d body=%s", late.Code, late.Body.String())
	}
	completeA.ExpectedVehicleVersion = 3
	completed := call(http.MethodPost, "/v2/fleet/vehicles/"+vehicleID+"/trips/completion", completeA)
	if completed.Code != 200 {
		t.Fatalf("complete status=%d body=%s", completed.Code, completed.Body.String())
	}
	var state, reservationState, zone string
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT operational_state,zone,vehicle_version FROM fleet_vehicles WHERE vehicle_id=$1`, vehicleID).Scan(&state, &zone, &version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state FROM fleet_reservations WHERE ride_id=$1`, rideA).Scan(&reservationState); err != nil {
		t.Fatal(err)
	}
	if state != "available" || zone != "centretown" || version != 4 || reservationState != "released" {
		t.Fatalf("completion not atomic: state=%s zone=%s version=%d reservation=%s", state, zone, version, reservationState)
	}
	releasedPosition := call(http.MethodGet, "/v2/fleet/vehicles/"+vehicleID+"/position", nil)
	var releasedState FleetPosition
	if releasedPosition.Code != 200 || json.Unmarshal(releasedPosition.Body.Bytes(), &releasedState) != nil || releasedState.OperationalState != "available" {
		t.Fatalf("released vehicle position status=%d state=%s body=%s", releasedPosition.Code, releasedState.OperationalState, releasedPosition.Body.String())
	}
	// A start replay returns its original receipt after completion advanced version and cleared ownership.
	replayed := call(http.MethodPost, "/v2/fleet/vehicles/"+vehicleID+"/trips/start", startA)
	var replayReceipt FleetEffectReceipt
	if replayed.Code != 200 || json.Unmarshal(replayed.Body.Bytes(), &replayReceipt) != nil || replayReceipt != startReceipt {
		t.Fatalf("start replay status=%d receipt=%+v want=%+v body=%s", replayed.Code, replayReceipt, startReceipt, replayed.Body.String())
	}
	changed := startA
	changed.RunID = "run-sim-other-" + suffix
	if w := call(http.MethodPost, "/v2/fleet/vehicles/"+vehicleID+"/trips/start", changed); w.Code != 409 {
		t.Fatalf("changed event payload status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := db.ExecContext(ctx, `UPDATE fleet_reservations SET vehicle_id=$2,state='reserved',reserved_at=$3,updated_at=$3,pickup_zone='centretown',passengers=1 WHERE ride_id=$1`, rideB, vehicleID, clock); err != nil {
		t.Fatal(err)
	}
	startB := fleetTripCommand{EventID: "event-sim-start-b-" + suffix, RunID: "run-sim-test-" + suffix, RideID: rideB, TripID: "trip-sim-b-" + suffix, VehicleID: vehicleID, ReservationID: rideB, RouteID: simulationRouteID, ExpectedVehicleVersion: 4}
	if w := call(http.MethodPost, "/v2/fleet/vehicles/"+vehicleID+"/trips/start", startB); w.Code != 409 {
		t.Fatalf("Centretown reservation must not teleport to Lansdowne status=%d body=%s", w.Code, w.Body.String())
	}
	late := completeA
	late.EventID, late.ExpectedVehicleVersion = "event-sim-late-a-"+suffix, 4
	if w := call(http.MethodPost, "/v2/fleet/vehicles/"+vehicleID+"/trips/completion", late); w.Code != 409 {
		t.Fatalf("late prior-trip event status=%d body=%s", w.Code, w.Body.String())
	}
	var activeTripID string
	if err := db.QueryRowContext(ctx, `SELECT operational_state,COALESCE(active_trip_id,'') FROM fleet_vehicles WHERE vehicle_id=$1`, vehicleID).Scan(&state, &activeTripID); err != nil {
		t.Fatal(err)
	}
	if state != "available" || activeTripID != "" {
		t.Fatalf("rejected event changed relocated vehicle: state=%s trip=%s", state, activeTripID)
	}
}
