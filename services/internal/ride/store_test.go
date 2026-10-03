package ride

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSubmitIsAtomicAndIdempotentAcrossRestart(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, cleanup := isolatedRideDB(t, raw)
	defer cleanup()
	ctx := context.Background()

	first, replay, err := NewStore(db).Submit(ctx, "test_key_0001", "req-test-001", Request{PickupZone: "lansdowne", DropoffZone: "centretown", Passengers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || replay.RideID == "" || replay.State != "queued" {
		t.Fatalf("unexpected accepted ride: %+v", replay)
	}
	second, existing, err := NewStore(db).Submit(ctx, "test_key_0001", "req-test-002", Request{PickupZone: "lansdowne", DropoffZone: "centretown", Passengers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || existing.RideID != replay.RideID {
		t.Fatalf("idempotent replay changed identity: %+v %+v", second, existing)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM assignment_jobs WHERE ride_id=$1`, replay.RideID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want exactly one durable job, got %d", count)
	}
	if _, _, err = NewStore(db).Submit(ctx, "test_key_0001", "req-test-003", Request{PickupZone: "glebe", DropoffZone: "centretown", Passengers: 2}); !IsConflict(err) {
		t.Fatalf("changed request should conflict, got %v", err)
	}

	// Competing callers with the same key converge on one committed ride and job.
	const callers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	ids := map[string]bool{}
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sub, r, e := NewStore(db).Submit(ctx, "concurrent_key_01", fmt.Sprintf("req-test-%03d", i+10), Request{PickupZone: "glebe", DropoffZone: "glebe", Passengers: 1})
			mu.Lock()
			defer mu.Unlock()
			if e != nil {
				errs[i] = e
				return
			}
			if sub.Created {
				created++
			}
			ids[r.RideID] = true
		}(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if created != 1 || len(ids) != 1 {
		t.Fatalf("concurrent idempotency created=%d identities=%d", created, len(ids))
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM assignment_jobs j JOIN rides r USING(ride_id) JOIN ride_idempotency i USING(ride_id) WHERE i.idempotency_key='concurrent_key_01'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent submit produced %d jobs", count)
	}

	// A forced job-insert failure must roll back the ride and leave the key unused.
	var before int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM rides`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION reject_job_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$; CREATE TRIGGER reject_job BEFORE INSERT ON assignment_jobs FOR EACH ROW EXECUTE FUNCTION reject_job_insert()`); err != nil {
		t.Fatal(err)
	}
	_, _, err = NewStore(db).Submit(ctx, "rollback_key_01", "req-test-rollback", Request{PickupZone: "centretown", DropoffZone: "glebe", Passengers: 1})
	if err == nil {
		t.Fatal("expected injected job failure")
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER reject_job ON assignment_jobs; DROP FUNCTION reject_job_insert()`); err != nil {
		t.Fatal(err)
	}
	var after, keys int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM rides`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ride_idempotency WHERE idempotency_key='rollback_key_01'`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if before != after || keys != 0 {
		t.Fatalf("partial acceptance persisted: rides %d->%d, keys=%d", before, after, keys)
	}
}

func TestRideRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request Request
		valid   bool
	}{
		{"valid", Request{"lansdowne", "centretown", 2}, true},
		{"unknown zone", Request{"other", "centretown", 2}, false},
		{"passenger floor", Request{"glebe", "glebe", 0}, false},
		{"passenger ceiling", Request{"glebe", "glebe", 5}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.request.Validate() == nil; got != tc.valid {
				t.Fatalf("Validate() valid=%v, want %v", got, tc.valid)
			}
		})
	}
}

func isolatedRideDB(t *testing.T, raw string) (*sql.DB, func()) {
	t.Helper()
	ctx := context.Background()
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("ride_test_%d", time.Now().UnixNano())
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
		t.Fatalf("apply ride migration: %v", err)
	}
	cleanup := func() {
		_ = db.Close()
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close()
	}
	return db, cleanup
}
