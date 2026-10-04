package ride

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/fleet"
)

func TestTripEffectsPersistIntentAndVersionedTransitions(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, cleanup := isolatedRideDB(t, raw)
	defer cleanup()
	ctx := context.Background()
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "ride", "002_trips.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply trip migration: %v", err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO rides(ride_id,pickup_zone,dropoff_zone,passengers,state,vehicle_id)
		VALUES('ride-test-001','lansdowne','centretown',2,'completed','vehicle-001');
		INSERT INTO assignment_jobs(job_id,ride_id,request_id,kind,payload,state,vehicle_id)
		VALUES('job-test-001','ride-test-001','req-test-001','assign_ride','{}','completed','vehicle-001')`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	command := TripCommand{EventID: "event-trip-start-001", TripID: "trip-test-001", VehicleID: "vehicle-001",
		ReservationID: "ride-test-001", RunID: "run-test-001", RouteID: tripRouteID,
		ExpectedTripVersion: 1, ExpectedVehicleVersion: 4}
	initial, err := store.GetTrip(ctx, command.ReservationID)
	if err != nil || initial.TripState != "not_started" || initial.Version != 1 || initial.AssignmentState != "completed" {
		t.Fatalf("initial trip=%+v err=%v", initial, err)
	}
	event, pending, err := store.PrepareTripStart(ctx, command.ReservationID, command)
	if err != nil || event.State != "pending" || pending.TripState != "not_started" {
		t.Fatalf("prepared event=%+v trip=%+v err=%v", event, pending, err)
	}
	if _, _, err = store.PrepareTripStart(ctx, command.ReservationID, command); err != nil {
		t.Fatalf("same event should replay its durable intent: %v", err)
	}
	other := command
	other.EventID = "event-trip-start-002"
	if _, _, err = store.PrepareTripStart(ctx, command.ReservationID, other); !errors.Is(err, ErrTripPending) {
		t.Fatalf("competing start should wait for pending event, got %v", err)
	}
	startReceipt := tripTestReceipt(command, "trip_start", fleetStartCommand(command), 5)
	started, err := store.CommitTripEffect(ctx, command.EventID, startReceipt)
	if err != nil || started.TripState != "in_progress" || started.Version != 2 || started.VehicleVersion != 5 {
		t.Fatalf("started trip=%+v err=%v", started, err)
	}
	completedCommand := command
	completedCommand.EventID = "event-trip-complete-001"
	completedCommand.ExpectedTripVersion = 2
	completedCommand.ExpectedVehicleVersion = 5
	completeEvent, _, err := store.PrepareTripCompletion(ctx, command.ReservationID, completedCommand)
	if err != nil || completeEvent.State != "pending" {
		t.Fatalf("completion intent=%+v err=%v", completeEvent, err)
	}
	completeReceipt := tripTestReceipt(completedCommand, "trip_complete", fleetCompleteCommand(completedCommand), 6)
	completed, err := store.CommitTripEffect(ctx, completedCommand.EventID, completeReceipt)
	if err != nil || completed.TripState != "completed" || completed.Version != 3 || completed.VehicleVersion != 6 {
		t.Fatalf("completed trip=%+v err=%v", completed, err)
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM ride_trip_effect_events WHERE ride_id='ride-test-001' AND state='accepted'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("accepted event count=%d err=%v", count, err)
	}
}

func TestTripCompletionRetriesAgainstFleetAfterRouteDurationAndReplaysAfterRestart(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, cleanup := isolatedRideDB(t, raw)
	defer cleanup()
	ctx := context.Background()
	rideMigration, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "ride", "002_trips.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(rideMigration)); err != nil {
		t.Fatalf("apply Ride trip migration: %v", err)
	}
	for _, name := range []string{"001_fleet.sql", "002_dataset_epoch.sql", "003_simulation.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "fleet", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, string(migration)); err != nil {
			t.Fatalf("apply Fleet migration %s: %v", name, err)
		}
	}
	const rideID, vehicleID = "ride-cross-service-001", "vehicle-cross-service-001"
	if _, err := db.ExecContext(ctx, `INSERT INTO rides(ride_id,pickup_zone,dropoff_zone,passengers,state,vehicle_id)
		VALUES($1,'lansdowne','centretown',2,'completed',$2)`, rideID, vehicleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO assignment_jobs(job_id,ride_id,request_id,kind,payload,state,vehicle_id)
		VALUES('job-cross-service-001',$1,'req-cross-service-001','assign_ride','{}','completed',$2)`, rideID, vehicleID); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_vehicles(vehicle_id,zone,seats,availability,observed_at,vehicle_version,operational_state)
		VALUES($1,'lansdowne',4,'available',$2,1,'reserved')`, vehicleID, clock); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_reservations(ride_id,vehicle_id,state,reserved_at,updated_at,pickup_zone,passengers)
		VALUES($1,$2,'reserved',$3,$3,'lansdowne',2)`, rideID, vehicleID, clock); err != nil {
		t.Fatal(err)
	}
	fleetServer := httptest.NewServer(fleet.NewHandler(db, func() time.Time { return clock }, fleet.FaultConfig{}))
	defer fleetServer.Close()
	store := NewStore(db)
	api := (&API{Store: store, Trips: NewHTTPTripEffects(fleetServer.URL)}).Handler()
	call := func(command TripCommand, completion bool) *httptest.ResponseRecorder {
		path := "/v2/rides/" + rideID + "/trip"
		if completion {
			path += "/completion"
		}
		body, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		return res
	}
	start := TripCommand{EventID: "event-cross-start-001", TripID: "trip-cross-service-001", VehicleID: vehicleID,
		ReservationID: rideID, RunID: "run-cross-service-001", RouteID: tripRouteID, ExpectedTripVersion: 1, ExpectedVehicleVersion: 1}
	if event, _, err := store.PrepareTripStart(ctx, rideID, start); err != nil || event.State != "pending" {
		t.Fatalf("pre-crash Ride intent event=%+v err=%v", event, err)
	}
	// Simulate a Ride crash after Fleet committed but before Ride stored the receipt.
	if result, err := NewHTTPTripEffects(fleetServer.URL).Start(ctx, fleetStartCommand(start)); err != nil || result.Status != http.StatusOK {
		t.Fatalf("Fleet pre-crash effect status=%d err=%v", result.Status, err)
	}
	api = (&API{Store: NewStore(db), Trips: NewHTTPTripEffects(fleetServer.URL)}).Handler()
	if res := call(start, false); res.Code != http.StatusOK {
		t.Fatalf("receipt recovery after Ride restart status=%d body=%s", res.Code, res.Body.String())
	}
	completion := start
	completion.EventID = "event-cross-complete-001"
	completion.ExpectedTripVersion = 2
	completion.ExpectedVehicleVersion = 2
	clock = clock.Add(7 * time.Second)
	if res := call(completion, true); res.Code != http.StatusServiceUnavailable {
		t.Fatalf("early completion status=%d body=%s", res.Code, res.Body.String())
	}
	var effectState string
	if err := db.QueryRowContext(ctx, `SELECT state FROM ride_trip_effect_events WHERE event_id=$1`, completion.EventID).Scan(&effectState); err != nil || effectState != "pending" {
		t.Fatalf("early completion must retain pending intent, state=%q err=%v", effectState, err)
	}
	clock = clock.Add(time.Second)
	if res := call(completion, true); res.Code != http.StatusOK {
		t.Fatalf("same-event completion retry status=%d body=%s", res.Code, res.Body.String())
	}
	// A new API/store instance represents a Ride process restart; accepted state is durable and no Fleet write is repeated.
	restarted := (&API{Store: NewStore(db), Trips: NewHTTPTripEffects(fleetServer.URL)}).Handler()
	body, _ := json.Marshal(completion)
	req := httptest.NewRequest(http.MethodPost, "/v2/rides/"+rideID+"/trip/completion", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	replay := httptest.NewRecorder()
	restarted.ServeHTTP(replay, req)
	if replay.Code != http.StatusOK {
		t.Fatalf("restart replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var trip Trip
	if err := json.Unmarshal(replay.Body.Bytes(), &trip); err != nil || trip.TripState != "completed" || trip.Version != 3 {
		t.Fatalf("restart replay trip=%+v err=%v", trip, err)
	}
	var receiptCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM fleet_simulation_effects WHERE event_id IN ($1,$2)`, start.EventID, completion.EventID).Scan(&receiptCount); err != nil || receiptCount != 2 {
		t.Fatalf("Fleet effect receipts=%d err=%v", receiptCount, err)
	}
}

func tripTestReceipt(command TripCommand, effect string, ownerCommand any, acceptedVersion int64) FleetEffectReceipt {
	fingerprint, _, _ := payloadFingerprint(ownerCommand)
	return FleetEffectReceipt{EventID: command.EventID, Effect: effect, RunID: command.RunID, RideID: command.ReservationID,
		TripID: command.TripID, VehicleID: command.VehicleID, ReservationID: command.ReservationID,
		PayloadFingerprint: fingerprint, AcceptedVehicleVersion: acceptedVersion, StoredAt: time.Now().UTC(), Result: "applied"}
}
