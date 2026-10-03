package worker

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/ride"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestConcurrentClaimsLeaseFencingAndCrashRecovery(t *testing.T) {
	db, cleanup := workerTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, created, err := ride.NewStore(db).Submit(ctx, "worker_crash_key_01", "req-worker-crash", ride.Request{PickupZone: "lansdowne", DropoffZone: "centretown", Passengers: 2})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make([]*Job, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claims[i], errs[i] = NewPostgresStore(db).Claim(ctx, fmt.Sprintf("worker-%03d", i))
		}(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if (claims[0] == nil) == (claims[1] == nil) {
		t.Fatalf("SKIP LOCKED claim results: %+v", claims)
	}
	first := claims[0]
	if first == nil {
		first = claims[1]
	}
	if first.RideID != created.RideID {
		t.Fatalf("claimed wrong ride %s", first.RideID)
	}
	fleet := &fakeFleet{}
	pauseCtx, cancel := context.WithCancel(ctx)
	w := testWorker(db, fleet)
	w.PauseAfterReservation = true
	done := make(chan struct{})
	go func() { w.process(pauseCtx, *first); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for fleet.reserveCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if _, err := db.ExecContext(ctx, `UPDATE assignment_jobs SET lease_expires_at=clock_timestamp()-interval '1 second',available_at=clock_timestamp() WHERE job_id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := NewPostgresStore(db).Claim(ctx, "worker-restarted")
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.Token == first.Token || second.Attempts != 2 {
		t.Fatalf("recovered lease was not fenced: %+v", second)
	}
	ok, err := NewPostgresStore(db).Complete(ctx, *first, "vehicle-001")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expired worker completed with an obsolete lease token")
	}
	w = testWorker(db, fleet)
	w.process(ctx, *second)
	if fleet.reserveCount() != 1 {
		t.Fatalf("crash recovery repeated reservation POST %d times", fleet.reserveCount())
	}
	var state, rideState, vehicle string
	if err := db.QueryRowContext(ctx, `SELECT j.state,r.state,j.vehicle_id FROM assignment_jobs j JOIN rides r USING(ride_id) WHERE j.job_id=$1`, first.ID).Scan(&state, &rideState, &vehicle); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || rideState != "completed" || vehicle != "vehicle-001" {
		t.Fatalf("recovery state job=%s ride=%s vehicle=%s", state, rideState, vehicle)
	}
}

func TestFifthAssignmentAttemptThenReconciliationWithoutSixthPost(t *testing.T) {
	db, cleanup := workerTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, accepted, err := ride.NewStore(db).Submit(ctx, "worker_retry_key_01", "req-worker-retry", ride.Request{PickupZone: "glebe", DropoffZone: "glebe", Passengers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE assignment_jobs SET attempts=4 WHERE ride_id=$1`, accepted.RideID); err != nil {
		t.Fatal(err)
	}
	fleet := &fakeFleet{reserveStatus: 503}
	w := testWorker(db, fleet)
	j, err := NewPostgresStore(db).Claim(ctx, "worker-attempt-5")
	if err != nil {
		t.Fatal(err)
	}
	if j == nil || j.Attempts != 5 || j.ReconcileOnly {
		t.Fatalf("fifth assignment attempt was skipped: %+v", j)
	}
	w.process(ctx, *j)
	if fleet.reserveCount() != 1 {
		t.Fatalf("expected fifth POST once, got %d", fleet.reserveCount())
	}
	if _, err = db.ExecContext(ctx, `UPDATE assignment_jobs SET lease_expires_at=clock_timestamp()-interval '1 second',available_at=clock_timestamp() WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	maintenance, err := NewPostgresStore(db).Claim(ctx, "worker-maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if maintenance == nil || !maintenance.ReconcileOnly || maintenance.Attempts != 5 {
		t.Fatalf("fifth attempt did not enter reconciliation: %+v", maintenance)
	}
	w.process(ctx, *maintenance)
	if fleet.reserveCount() != 1 {
		t.Fatalf("maintenance performed sixth POST, count=%d", fleet.reserveCount())
	}
	if fleet.releaseCount() != 1 {
		t.Fatalf("expected tombstone cleanup, DELETE count=%d", fleet.releaseCount())
	}
	var code, state string
	if err := db.QueryRowContext(ctx, `SELECT j.failure_code,j.state FROM assignment_jobs j WHERE j.job_id=$1`, j.ID).Scan(&code, &state); err != nil {
		t.Fatal(err)
	}
	if code != "retry_exhausted" || state != "failed" {
		t.Fatalf("want retry_exhausted failed, got %s %s", code, state)
	}
}

func TestMalformedJobIsReleasedAndFailed(t *testing.T) {
	db, cleanup := workerTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, accepted, err := ride.NewStore(db).Submit(ctx, "worker_badjob_key_01", "req-worker-badjob", ride.Request{PickupZone: "glebe", DropoffZone: "glebe", Passengers: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE assignment_jobs SET payload='{}'::jsonb WHERE ride_id=$1`, accepted.RideID); err != nil {
		t.Fatal(err)
	}
	fleet := &fakeFleet{existing: Reservation{RideID: accepted.RideID, VehicleID: "vehicle-001", State: "reserved"}}
	w := testWorker(db, fleet)
	j, err := NewPostgresStore(db).Claim(ctx, "worker-invalid")
	if err != nil {
		t.Fatal(err)
	}
	if j == nil || !j.Invalid {
		t.Fatalf("malformed payload was not marked invalid: %+v", j)
	}
	w.process(ctx, *j)
	if fleet.reserveCount() != 0 || fleet.releaseCount() != 1 {
		t.Fatalf("invalid job effects: POST=%d DELETE=%d", fleet.reserveCount(), fleet.releaseCount())
	}
	var state, code string
	if err := db.QueryRowContext(ctx, `SELECT state,failure_code FROM assignment_jobs WHERE job_id=$1`, j.ID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != "invalid_job" {
		t.Fatalf("got %s/%s", state, code)
	}
}

func TestCleanupOutageBeforeAttemptFiveNeverReposts(t *testing.T) {
	db, cleanup := workerTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, accepted, err := ride.NewStore(db).Submit(ctx, "worker_cleanup_key_01", "req-worker-cleanup", ride.Request{PickupZone: "glebe", DropoffZone: "glebe", Passengers: 1})
	if err != nil {
		t.Fatal(err)
	}
	fleet := &fakeFleet{reserveStatus: 409, reserveCode: "no_capacity", releaseStatus: 503}
	w := testWorker(db, fleet)
	first, err := NewPostgresStore(db).Claim(ctx, "worker-cleanup-1")
	if err != nil {
		t.Fatal(err)
	}
	w.process(ctx, *first)
	if fleet.reserveCount() != 1 || fleet.releaseCount() != 1 {
		t.Fatalf("initial definitive failure calls: POST=%d DELETE=%d", fleet.reserveCount(), fleet.releaseCount())
	}
	if _, err = db.ExecContext(ctx, `UPDATE assignment_jobs SET lease_expires_at=clock_timestamp()-interval '1 second',available_at=clock_timestamp() WHERE job_id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	maintenance, err := NewPostgresStore(db).Claim(ctx, "worker-cleanup-2")
	if err != nil {
		t.Fatal(err)
	}
	if maintenance == nil || !maintenance.ReconcileOnly || maintenance.Attempts != 1 || maintenance.PendingFailureCode != "no_capacity" {
		t.Fatalf("cleanup outage did not preserve maintenance mode: %+v", maintenance)
	}
	fleet.releaseStatus = 200
	w.process(ctx, *maintenance)
	if fleet.reserveCount() != 1 {
		t.Fatalf("maintenance repeated POST, count=%d", fleet.reserveCount())
	}
	var code string
	if err := db.QueryRowContext(ctx, `SELECT failure_code FROM assignment_jobs WHERE ride_id=$1`, accepted.RideID).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "no_capacity" {
		t.Fatalf("terminal code=%s", code)
	}
}

type fakeFleet struct {
	mu                   sync.Mutex
	getStatus            int
	reserveStatus        int
	reserveCode          string
	releaseStatus        int
	existing             Reservation
	posts, deletes, gets int
}

func (f *fakeFleet) Get(_ context.Context, id, _ string) (FleetResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.existing.State == "reserved" {
		return FleetResult{Status: 200, Reservation: f.existing}, nil
	}
	status := f.getStatus
	if status == 0 {
		status = 404
	}
	return FleetResult{Status: status}, nil
}
func (f *fakeFleet) Reserve(_ context.Context, j Job, _ string) (FleetResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts++
	status := f.reserveStatus
	if status == 0 {
		status = 201
		f.existing = Reservation{RideID: j.RideID, VehicleID: "vehicle-001", State: "reserved"}
	}
	return FleetResult{Status: status, Reservation: f.existing, Code: f.reserveCode}, nil
}
func (f *fakeFleet) Release(_ context.Context, id, _ string) (FleetResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	status := f.releaseStatus
	if status == 0 {
		status = 200
	}
	if status >= 200 && status < 300 {
		f.existing = Reservation{RideID: id, State: "released"}
	}
	return FleetResult{Status: status}, nil
}
func (f *fakeFleet) reserveCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.posts }
func (f *fakeFleet) releaseCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.deletes }

func testWorker(db *sql.DB, fleet Fleet) *Worker {
	return &Worker{Store: NewPostgresStore(db), Fleet: fleet, Owner: "worker-tests", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}
func workerTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("worker_test_%d", time.Now().UnixNano())
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(16)
	if err = db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "ride", "001_ride_jobs.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = db.Close()
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	}
	return db, cleanup
}
