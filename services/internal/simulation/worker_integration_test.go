package simulation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
)

func TestDispatcherReconcilesPendingStartAcrossStopAndCompletesPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.ApplyMigrations(ctx, db, filepath.Join("..", "..", "..", "db")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	tripState := "not_started"
	var tripStarted time.Time
	var ownerTripID string
	startCalls := 0
	completeCalls := 0
	clock := time.Now().UTC().Truncate(time.Microsecond)
	var rideServer *httptest.Server
	rideServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/rides":
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": "ride-simulation-dispatch", "state": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/rides/ride-simulation-dispatch":
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": "ride-simulation-dispatch", "state": "completed", "vehicle_id": "vehicle-001"})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/rides/ride-simulation-dispatch/trip":
			started := tripStarted
			if started.IsZero() {
				started = clock
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": "ride-simulation-dispatch", "assignment_state": "completed", "trip_state": tripState, "trip_id": ownerTripID, "vehicle_id": "vehicle-001", "start_zone": "lansdowne", "destination_zone": "centretown", "started_at": started.UTC().Format(time.RFC3339Nano), "updated_at": clock.UTC().Format(time.RFC3339Nano), "version": map[string]int{"not_started": 1, "in_progress": 2, "completed": 3}[tripState]})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/rides/ride-simulation-dispatch/trip":
			startCalls++
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(map[string]any{"trip_state": "not_started"})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/rides/ride-simulation-dispatch/trip/completion":
			completeCalls++
			tripState = "completed"
			_ = json.NewEncoder(w).Encode(map[string]any{"trip_state": "completed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer rideServer.Close()
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/fleet" {
			items := make([]map[string]string, 20)
			for i := range items {
				items[i] = map[string]string{"vehicle_id": fmt.Sprintf("vehicle-%03d", i+1)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": nil})
			return
		}
		if r.URL.Path == "/v2/fleet/vehicles/vehicle-001/position" {
			mu.Lock()
			defer mu.Unlock()
			state, version := "reserved", 1
			if tripState != "not_started" {
				state, version = "on_trip", 2
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operational_state": state, "vehicle_version": version})
			return
		}
		http.NotFound(w, r)
	}))
	defer fleet.Close()
	h := NewHandler(db, Config{Enabled: true, FleetURL: fleet.URL, RideURL: rideServer.URL, Clock: func() time.Time { return clock }})
	manifest := Manifest{ScenarioID: "route20synthetic", FleetProfile: "route20synthetic", IdempotencyKey: "dispatch-test-" + fmt.Sprint(time.Now().UnixNano()), EventRatePerSecond: 4, RequestCount: 1, ExecutionEventCount: 2, DurationSeconds: 60, MaxInFlight: 1, RouteDurationSeconds: 8, RouteID: routeID, StartZone: "lansdowne", EndZone: "centretown", Seed: 1}
	body, _ := json.Marshal(map[string]any{"manifest": manifest})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v2/simulation/runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var run Run
	if err = json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT trip_id FROM simulation_requests WHERE run_id=$1 AND sequence=1`, run.RunID).Scan(&ownerTripID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM simulation_events WHERE run_id=$1`, run.RunID)
		_, _ = db.Exec(`DELETE FROM simulation_requests WHERE run_id=$1`, run.RunID)
		_, _ = db.Exec(`DELETE FROM simulation_runs WHERE run_id=$1`, run.RunID)
	}()
	if err = h.advance(ctx); err != nil {
		t.Fatal(err)
	} // persist Ride request identity
	if err = h.advance(ctx); err == nil {
		t.Fatal("202 pending Ride response was incorrectly treated as an applied trip start")
	} // payload is durable while Ride still reports not_started
	var result string
	var storedPayload, storedIssued bool
	var issued int
	if err = db.QueryRow(`SELECT result FROM simulation_events WHERE run_id=$1 AND kind='trip_start'`, run.RunID).Scan(&result); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT payload IS NOT NULL,issued_at IS NOT NULL FROM simulation_events WHERE run_id=$1 AND kind='trip_start'`, run.RunID).Scan(&storedPayload, &storedIssued); err != nil {
		t.Fatal(err)
	}
	if result != "pending" {
		t.Fatalf("202 start was marked %q", result)
	}
	if err = db.QueryRow(`SELECT issued_events FROM simulation_runs WHERE run_id=$1`, run.RunID).Scan(&issued); err != nil || issued != 1 {
		var assignment, trip string
		_ = db.QueryRow(`SELECT assignment_state,trip_state FROM simulation_requests WHERE run_id=$1`, run.RunID).Scan(&assignment, &trip)
		var due time.Time
		_ = db.QueryRow(`SELECT scheduled_at FROM simulation_requests WHERE run_id=$1`, run.RunID).Scan(&due)
		var recent int
		rateErr := db.QueryRow(`SELECT count(*) FROM simulation_events WHERE run_id=$1 AND issued_at>$2::timestamptz-interval '1 second'`, run.RunID, clock).Scan(&recent)
		t.Fatalf("ledger-derived issued=%d payload=%v issued_at=%v request=%s/%s starts=%d due=%s now=%s rateLimited=%v count=%d rateErr=%v err=%v", issued, storedPayload, storedIssued, assignment, trip, startCalls, due.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano), h.rateLimited(ctx, run.RunID, 4, clock), recent, rateErr, err)
	}
	var disabledState string
	if err = db.QueryRow(`SELECT state FROM simulation_runs WHERE run_id=$1`, run.RunID).Scan(&disabledState); err != nil {
		t.Fatal(err)
	}
	disabled := NewHandler(db, Config{Enabled: false, FleetURL: fleet.URL, RideURL: rideServer.URL, Clock: func() time.Time { return clock }})
	workerCtx, workerCancel := context.WithCancel(ctx)
	workerCancel()
	if err = disabled.RunWorker(workerCtx); err != nil {
		t.Fatalf("disabled dispatcher returned %v", err)
	}
	if err = db.QueryRow(`SELECT state FROM simulation_runs WHERE run_id=$1`, run.RunID).Scan(&disabledState); err != nil || disabledState != "running" {
		t.Fatalf("disabled dispatcher changed run state=%s err=%v", disabledState, err)
	}
	stop := httptest.NewRequest(http.MethodDelete, "/v2/simulation/runs/"+run.RunID, bytes.NewBufferString(`{"drain":true}`))
	stop.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.Handler().ServeHTTP(w, stop)
	if w.Code != 200 {
		t.Fatalf("stop %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	tripState = "in_progress"
	tripStarted = clock
	started := clock
	mu.Unlock()
	// A fresh handler has no in-memory progress and must reconcile the stored start payload.
	h = NewHandler(db, Config{Enabled: true, FleetURL: fleet.URL, RideURL: rideServer.URL, Clock: func() time.Time { return clock }})
	if err = h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT result FROM simulation_events WHERE run_id=$1 AND kind='trip_start'`, run.RunID).Scan(&result); err != nil || result != "applied" {
		t.Fatalf("persisted start not reconciled: result=%s err=%v", result, err)
	}
	clock = started.Add(8 * time.Second)
	if err = h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var completeCount, issuedCount int
	if err = db.QueryRow(`SELECT state,completed_events,issued_events FROM simulation_runs WHERE run_id=$1`, run.RunID).Scan(&state, &completeCount, &issuedCount); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" || completeCount != 2 || issuedCount != 2 || startCalls != 1 || completeCalls != 1 {
		t.Fatalf("drained run state=%s completed=%d issued=%d starts=%d completions=%d", state, completeCount, issuedCount, startCalls, completeCalls)
	}
}

func TestCompletedRequestReleasesInflightSlotPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.ApplyMigrations(ctx, db, filepath.Join("..", "..", "..", "db")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	posts := 0
	ride := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/rides" {
			posts++
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"ride_id": fmt.Sprintf("ride-slot-%d", posts), "state": "queued"})
			return
		}
		http.NotFound(w, r)
	}))
	defer ride.Close()
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v1/fleet" {
			items := make([]map[string]string, 20)
			for i := range items {
				items[i] = map[string]string{"vehicle_id": fmt.Sprintf("vehicle-%03d", i+1)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": nil})
			return
		}
		http.NotFound(w, r)
	}))
	defer fleet.Close()
	h := NewHandler(db, Config{Enabled: true, FleetURL: fleet.URL, RideURL: ride.URL, Clock: func() time.Time { return now }})
	m := Manifest{ScenarioID: "route20synthetic", FleetProfile: "route20synthetic", IdempotencyKey: "slot-test-" + fmt.Sprint(time.Now().UnixNano()), EventRatePerSecond: 4, RequestCount: 2, ExecutionEventCount: 4, DurationSeconds: 60, MaxInFlight: 1, RouteDurationSeconds: 8, RouteID: routeID, StartZone: "lansdowne", EndZone: "centretown", Seed: 1}
	body, _ := json.Marshal(map[string]any{"manifest": m})
	w := httptest.NewRecorder()
	create := httptest.NewRequest(http.MethodPost, "/v2/simulation/runs", bytes.NewReader(body))
	create.Header.Set("Content-Type", "application/json")
	h.Handler().ServeHTTP(w, create)
	if w.Code != http.StatusOK {
		t.Fatalf("create run: %d %s", w.Code, w.Body.String())
	}
	var run Run
	if err = json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(`DELETE FROM simulation_events WHERE run_id=$1`, run.RunID)
		_, _ = db.Exec(`DELETE FROM simulation_requests WHERE run_id=$1`, run.RunID)
		_, _ = db.Exec(`DELETE FROM simulation_runs WHERE run_id=$1`, run.RunID)
	}()
	if err = h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("expected first request dispatch, got %d", posts)
	}
	// Model a durably completed assignment while its Ride assignment remains
	// assigned; terminal request state must free the only in-flight slot.
	if _, err = db.Exec(`UPDATE simulation_requests SET assignment_state='assigned',trip_state='completed',ride_id='ride-slot-1',vehicle_id='vehicle-001' WHERE run_id=$1 AND sequence=1`, run.RunID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err = h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	if posts != 2 {
		t.Fatalf("completed request held max_in_flight slot: expected 2 Ride submissions, got %d", posts)
	}
	var state string
	if err = db.QueryRow(`SELECT assignment_state FROM simulation_requests WHERE run_id=$1 AND sequence=2`, run.RunID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" {
		t.Fatalf("second request state=%q, want accepted", state)
	}
}
