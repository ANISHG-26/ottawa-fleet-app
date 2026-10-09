package simulation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
)

func TestStopFencesUnissuedStartAfterWorkerReadPostgres(t *testing.T) {
	db := openStopRecoveryDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	runID := fmt.Sprintf("run-stop-fence-%d", now.UnixNano())
	rideID, tripID := testRideID(runID), testTripID(runID)
	insertStopRecoveryRun(t, db, runID, "running", now, true)
	defer deleteStopRecoveryRun(t, db, runID)

	entered := make(chan struct{})
	resume := make(chan struct{})
	var enteredOnce sync.Once
	var starts atomic.Int32
	ride := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v2/rides/"+rideID+"/trip" {
			enteredOnce.Do(func() {
				close(entered)
				<-resume
			})
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": rideID, "assignment_state": "completed", "trip_state": "not_started", "trip_id": tripID, "vehicle_id": "vehicle-001", "version": 1})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v2/rides/"+rideID+"/trip" {
			starts.Add(1)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		http.NotFound(w, r)
	}))
	defer ride.Close()
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/fleet/vehicles/vehicle-001/position" {
			_ = json.NewEncoder(w).Encode(map[string]any{"operational_state": "reserved", "vehicle_version": 1})
			return
		}
		http.NotFound(w, r)
	}))
	defer fleet.Close()
	h := NewHandler(db, Config{Enabled: true, FleetURL: fleet.URL, RideURL: ride.URL, Clock: func() time.Time { return now }})
	advanced := make(chan error, 1)
	go func() { advanced <- h.advance(ctx) }()
	<-entered // the worker has read state=running and is paused before payload issuance
	stop := httptest.NewRecorder()
	stopRequest := httptest.NewRequest(http.MethodDelete, "/v2/simulation/runs/"+runID, strings.NewReader(`{"drain":true}`))
	stopRequest.Header.Set("Content-Type", "application/json")
	h.stopRun(stop, stopRequest, runID)
	if stop.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", stop.Code, stop.Body.String())
	}
	close(resume)
	_ = <-advanced
	var payloadPresent bool
	if err := db.QueryRow(`SELECT payload IS NOT NULL FROM simulation_events WHERE run_id=$1 AND kind='trip_start'`, runID).Scan(&payloadPresent); err != nil {
		t.Fatal(err)
	}
	if payloadPresent || starts.Load() != 0 {
		t.Fatalf("stop did not fence new start issuance: payload=%v calls=%d", payloadPresent, starts.Load())
	}
}

func TestDeadlineSnapshotCountsIssuedStartAndReconcilesAfterStopPostgres(t *testing.T) {
	db := openStopRecoveryDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	runID := fmt.Sprintf("run-stop-recover-%d", now.UnixNano())
	rideID, tripID := testRideID(runID), testTripID(runID)
	insertStopRecoveryRun(t, db, runID, "stopping", now, true)
	defer deleteStopRecoveryRun(t, db, runID)
	eventID := fmt.Sprintf("evt-%s-1", runID)
	if _, err := db.Exec(`UPDATE simulation_events SET payload='{}'::jsonb,issued_at=$2 WHERE event_id=$1`, eventID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO simulation_requests(run_id,sequence,idempotency_key,trip_id,scheduled_at,assignment_state,trip_state) VALUES($1,2,$2,'trip-cancelled',$3,'failed','cancelled')`, runID, runID+"-cancelled", now); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, Config{Enabled: true, Clock: func() time.Time { return now }})
	if err := h.finishRun(ctx, runID, "stopping", "requested_stop", now.Add(-time.Second), now); err != nil {
		t.Fatal(err)
	}
	var state, tripState string
	var incompleteTrips, incompleteRequests, incompleteEvents int
	if err := db.QueryRow(`SELECT state,incomplete_trips,incomplete_requests,incomplete_events FROM simulation_runs WHERE run_id=$1`, runID).Scan(&state, &incompleteTrips, &incompleteRequests, &incompleteEvents); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" || incompleteTrips != 1 || incompleteRequests != 1 || incompleteEvents != 2 {
		t.Fatalf("deadline snapshot state=%s incomplete trips/requests/events=%d/%d/%d", state, incompleteTrips, incompleteRequests, incompleteEvents)
	}
	if err := db.QueryRow(`SELECT trip_state FROM simulation_requests WHERE run_id=$1 AND sequence=1`, runID).Scan(&tripState); err != nil || tripState != "unfinished" {
		t.Fatalf("issued start not represented as unfinished: state=%s err=%v", tripState, err)
	}
	var ridePosts atomic.Int32
	var tripMu sync.Mutex
	tripState = "not_started"
	startedAt := now
	ride := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v2/rides/"+rideID+"/trip" {
			tripMu.Lock()
			current := tripState
			tripMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": rideID, "assignment_state": "completed", "trip_state": current, "trip_id": tripID, "vehicle_id": "vehicle-001", "started_at": startedAt.Format(time.RFC3339Nano), "version": map[string]int{"not_started": 1, "in_progress": 2, "completed": 3}[current]})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v2/rides/"+rideID+"/trip" {
			ridePosts.Add(1)
			tripMu.Lock()
			tripState = "in_progress"
			tripMu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v2/rides/"+rideID+"/trip/completion" {
			ridePosts.Add(1)
			tripMu.Lock()
			tripState = "completed"
			tripMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"trip_state": "completed"})
			return
		}
		http.NotFound(w, r)
	}))
	defer ride.Close()
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/fleet/vehicles/vehicle-001/position" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"operational_state": "on_trip", "vehicle_version": 2})
			return
		}
		http.NotFound(w, r)
	}))
	defer fleet.Close()
	h = NewHandler(db, Config{Enabled: true, RideURL: ride.URL, FleetURL: fleet.URL, Clock: func() time.Time { return now }})
	if err := h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := db.QueryRow(`SELECT result FROM simulation_events WHERE event_id=$1`, eventID).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "applied" || ridePosts.Load() != 1 {
		t.Fatalf("terminal reconciliation result=%s stable Ride replay calls=%d", result, ridePosts.Load())
	}
	now = now.Add(9 * time.Second)
	if err := h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	var completionResult string
	if err := db.QueryRow(`SELECT result FROM simulation_events WHERE run_id=$1 AND kind='trip_complete'`, runID).Scan(&completionResult); err != nil {
		t.Fatal(err)
	}
	tripMu.Lock()
	finalTripState := tripState
	tripMu.Unlock()
	if completionResult != "applied" || finalTripState != "completed" || ridePosts.Load() != 2 {
		t.Fatalf("pre-created completion did not converge: result=%s trip=%s owner posts=%d", completionResult, finalTripState, ridePosts.Load())
	}
	if err := db.QueryRow(`SELECT incomplete_trips,incomplete_requests,incomplete_events FROM simulation_runs WHERE run_id=$1`, runID).Scan(&incompleteTrips, &incompleteRequests, &incompleteEvents); err != nil {
		t.Fatal(err)
	}
	if incompleteTrips != 1 || incompleteRequests != 1 || incompleteEvents != 2 {
		t.Fatalf("terminal snapshot changed after reconciliation: trips/requests/events=%d/%d/%d", incompleteTrips, incompleteRequests, incompleteEvents)
	}
	var cancelledTrip, cancelledAssignment string
	if err := db.QueryRow(`SELECT trip_state,assignment_state FROM simulation_requests WHERE run_id=$1 AND sequence=2`, runID).Scan(&cancelledTrip, &cancelledAssignment); err != nil || cancelledTrip != "cancelled" || cancelledAssignment != "failed" {
		t.Fatalf("cancelled request was reclassified: state=%s assignment=%s err=%v", cancelledTrip, cancelledAssignment, err)
	}
}

func TestStopFencesNewDemandAndReplaysClaimedSubmissionPostgres(t *testing.T) {
	db := openStopRecoveryDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	runID := fmt.Sprintf("run-stop-demand-%d", now.UnixNano())
	insertStopRecoveryRun(t, db, runID, "stopping", now, false)
	defer deleteStopRecoveryRun(t, db, runID)
	var posts atomic.Int32
	ride := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ride_id": testRideID(runID), "state": "queued"})
	}))
	defer ride.Close()
	h := NewHandler(db, Config{Enabled: true, RideURL: ride.URL, Clock: func() time.Time { return now }})
	x := workerRequest{seq: 1, key: runID + "-request", scheduled: now, assignment: "pending", tripState: "not_started"}
	if err := h.submitRide(ctx, runID, x); err != nil {
		t.Fatal(err)
	}
	var submitted sql.NullTime
	if err := db.QueryRow(`SELECT submitted_at FROM simulation_requests WHERE run_id=$1`, runID).Scan(&submitted); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 0 || submitted.Valid {
		t.Fatalf("unissued demand crossed Stop: POSTs=%d submitted_at=%v", posts.Load(), submitted.Valid)
	}
	if _, err := db.Exec(`UPDATE simulation_requests SET submitted_at=$2 WHERE run_id=$1`, runID, now); err != nil {
		t.Fatal(err)
	}
	if err := h.finishRun(ctx, runID, "stopping", "requested_stop", now.Add(-time.Second), now); err != nil {
		t.Fatal(err)
	}
	if err := h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	var storedRide, assignment string
	if err := db.QueryRow(`SELECT ride_id,assignment_state FROM simulation_requests WHERE run_id=$1`, runID).Scan(&storedRide, &assignment); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 || storedRide != testRideID(runID) || assignment != "accepted" {
		t.Fatalf("pre-Stop submission not reconciled through advance: POSTs=%d ride=%s assignment=%s", posts.Load(), storedRide, assignment)
	}
}

func TestTerminalReconciliationRotatesPastUnavailableRunPostgres(t *testing.T) {
	db := openStopRecoveryDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	firstID := fmt.Sprintf("run-round-robin-a-%d", now.UnixNano())
	secondID := fmt.Sprintf("run-round-robin-b-%d", now.UnixNano())
	insertStopRecoveryRun(t, db, firstID, "stopped", now, true)
	if _, err := db.Exec(`UPDATE simulation_requests SET ride_id='ride-round-robin-a' WHERE run_id=$1`, firstID); err != nil {
		t.Fatal(err)
	}
	insertStopRecoveryRun(t, db, secondID, "stopped", now.Add(time.Microsecond), true)
	defer deleteStopRecoveryRun(t, db, firstID)
	defer deleteStopRecoveryRun(t, db, secondID)
	if _, err := db.Exec(`UPDATE simulation_requests SET ride_id=CASE run_id WHEN $1 THEN 'ride-round-robin-a' ELSE 'ride-round-robin-b' END WHERE run_id IN ($1,$2)`, firstID, secondID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{firstID, secondID} {
		if _, err := db.Exec(`UPDATE simulation_events SET payload='{}'::jsonb,issued_at=$2 WHERE run_id=$1 AND kind='trip_start'`, id, now); err != nil {
			t.Fatal(err)
		}
	}
	ride := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v2/rides/ride-round-robin-a/trip" {
			http.Error(w, "owner unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/v2/rides/ride-round-robin-b/trip" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": "ride-round-robin-b", "assignment_state": "completed", "trip_state": "in_progress", "trip_id": testTripID(secondID), "vehicle_id": "vehicle-001", "version": 2})
			return
		}
		http.NotFound(w, r)
	}))
	defer ride.Close()
	h := NewHandler(db, Config{Enabled: true, RideURL: ride.URL, Clock: func() time.Time { return now }})
	if err := h.advance(ctx); err == nil {
		t.Fatal("expected first terminal owner call to fail")
	}
	if err := h.advance(ctx); err != nil {
		t.Fatalf("second terminal run was starved by first: %v", err)
	}
	var result string
	if err := db.QueryRow(`SELECT result FROM simulation_events WHERE run_id=$1 AND kind='trip_start'`, secondID).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if result != "applied" {
		t.Fatalf("later terminal run did not reconcile: %s", result)
	}
}

func TestTerminalCompletesTripStartedBeforeDeadlinePostgres(t *testing.T) {
	db := openStopRecoveryDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	startedAt := now.Add(-5 * time.Second)
	runID := fmt.Sprintf("run-stop-inprogress-%d", now.UnixNano())
	rideID, tripID := testRideID(runID), testTripID(runID)
	insertStopRecoveryRun(t, db, runID, "stopping", now, true)
	defer deleteStopRecoveryRun(t, db, runID)
	if _, err := db.Exec(`UPDATE simulation_requests SET trip_state='in_progress' WHERE run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE simulation_events SET result='applied',issued_at=$2,payload='{}'::jsonb WHERE run_id=$1 AND kind='trip_start'`, runID, startedAt); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, Config{Enabled: true, Clock: func() time.Time { return now }})
	if err := h.finishRun(ctx, runID, "stopping", "requested_stop", now.Add(-time.Second), now); err != nil {
		t.Fatal(err)
	}
	var state, requestState string
	var incompleteTrips, incompleteEvents int
	if err := db.QueryRow(`SELECT state,incomplete_trips,incomplete_events FROM simulation_runs WHERE run_id=$1`, runID).Scan(&state, &incompleteTrips, &incompleteEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT trip_state FROM simulation_requests WHERE run_id=$1`, runID).Scan(&requestState); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" || requestState != "unfinished" || incompleteTrips != 1 || incompleteEvents != 1 {
		t.Fatalf("deadline state=%s request=%s incomplete trips/events=%d/%d", state, requestState, incompleteTrips, incompleteEvents)
	}
	var tripMu sync.Mutex
	tripState := "in_progress"
	completionPosts := 0
	ride := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v2/rides/"+rideID+"/trip" {
			tripMu.Lock()
			current := tripState
			tripMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": rideID, "assignment_state": "completed", "trip_state": current, "trip_id": tripID, "vehicle_id": "vehicle-001", "started_at": startedAt.Format(time.RFC3339Nano), "version": map[string]int{"in_progress": 2, "completed": 3}[current]})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v2/rides/"+rideID+"/trip/completion" {
			tripMu.Lock()
			tripState = "completed"
			completionPosts++
			tripMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"trip_state": "completed"})
			return
		}
		http.NotFound(w, r)
	}))
	defer ride.Close()
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/fleet/vehicles/vehicle-001/position" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"operational_state": "on_trip", "vehicle_version": 2})
			return
		}
		http.NotFound(w, r)
	}))
	defer fleet.Close()
	resumeAt := now.Add(3 * time.Second)
	h = NewHandler(db, Config{Enabled: true, RideURL: ride.URL, FleetURL: fleet.URL, Clock: func() time.Time { return resumeAt }})
	if err := h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	var completionResult string
	if err := db.QueryRow(`SELECT result FROM simulation_events WHERE run_id=$1 AND kind='trip_complete'`, runID).Scan(&completionResult); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT trip_state FROM simulation_requests WHERE run_id=$1`, runID).Scan(&requestState); err != nil {
		t.Fatal(err)
	}
	tripMu.Lock()
	posts, finalState := completionPosts, tripState
	tripMu.Unlock()
	if completionResult != "applied" || requestState != "completed" || finalState != "completed" || posts != 1 {
		t.Fatalf("terminal completion result=%s request=%s Ride=%s posts=%d", completionResult, requestState, finalState, posts)
	}
	if err := db.QueryRow(`SELECT incomplete_trips,incomplete_events FROM simulation_runs WHERE run_id=$1`, runID).Scan(&incompleteTrips, &incompleteEvents); err != nil {
		t.Fatal(err)
	}
	if incompleteTrips != 1 || incompleteEvents != 1 {
		t.Fatalf("terminal snapshot changed after completion: trips/events=%d/%d", incompleteTrips, incompleteEvents)
	}
}

func Test000StopRecoveryFixtureCleanupPostgres(t *testing.T) {
	db := openStopRecoveryDB(t)
	ctx := context.Background()
	for _, prefix := range []string{"run-stop-fence-%", "run-stop-recover-%", "run-stop-demand-%", "run-round-robin-%", "run-stop-inprogress-%"} {
		if _, err := db.ExecContext(ctx, `DELETE FROM simulation_events WHERE run_id IN (SELECT run_id FROM simulation_runs WHERE run_id LIKE $1)`, prefix); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM simulation_requests WHERE run_id IN (SELECT run_id FROM simulation_runs WHERE run_id LIKE $1)`, prefix); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM simulation_runs WHERE run_id LIKE $1`, prefix); err != nil {
			t.Fatal(err)
		}
	}
}

func openStopRecoveryDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := database.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.ApplyMigrations(context.Background(), db, filepath.Join("..", "..", "..", "db")); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertStopRecoveryRun(t *testing.T, db *sql.DB, runID, state string, now time.Time, assigned bool) {
	t.Helper()
	manifest := `{"scenario_id":"route20synthetic","fleet_profile":"route20synthetic","idempotency_key":"stop-recovery-test","event_rate_per_second":4,"request_count":1,"execution_event_count":2,"duration_seconds":60,"max_in_flight":1,"route_duration_seconds":8,"route_id":"lansdowne-centretown-v1","start_zone":"lansdowne","end_zone":"centretown","seed":1}`
	_, err := db.Exec(`INSERT INTO simulation_runs(run_id,idempotency_key,manifest_fingerprint,manifest,state,created_at,deadline_at,drain_deadline_at) VALUES($1,$2,repeat('a',64),$3::jsonb,$4,$5,$6,$7)`, runID, runID+"-idem", manifest, state, now, now.Add(time.Minute), now.Add(70*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assignment := "pending"
	var rideID *string
	var submitted *time.Time
	if assigned {
		assignment = "assigned"
		rideValue, submittedValue := testRideID(runID), now
		rideID, submitted = &rideValue, &submittedValue
	}
	_, err = db.Exec(`INSERT INTO simulation_requests(run_id,sequence,idempotency_key,trip_id,scheduled_at,assignment_state,ride_id,submitted_at,vehicle_id) VALUES($1,1,$2,$3,$4,$5,$6,$7,'vehicle-001')`, runID, runID+"-request", testTripID(runID), now, assignment, rideID, submitted)
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []string{"trip_start", "trip_complete"} {
		_, err = db.Exec(`INSERT INTO simulation_events(event_id,run_id,sequence,request_sequence,kind,trip_id,scheduled_at) VALUES($1,$2,$3,1,$4,$5,$6)`, fmt.Sprintf("evt-%s-%d", runID, i+1), runID, i+1, kind, testTripID(runID), now)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func testRideID(runID string) string { return "ride-" + strings.TrimPrefix(runID, "run-") }

func testTripID(runID string) string { return "trip-" + strings.TrimPrefix(runID, "run-") }

func deleteStopRecoveryRun(t *testing.T, db *sql.DB, runID string) {
	t.Helper()
	_, _ = db.Exec(`DELETE FROM simulation_events WHERE run_id=$1`, runID)
	_, _ = db.Exec(`DELETE FROM simulation_requests WHERE run_id=$1`, runID)
	_, _ = db.Exec(`DELETE FROM simulation_runs WHERE run_id=$1`, runID)
}
