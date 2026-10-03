package database

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
)

func TestApplyMigrationsConcurrentInitialization(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	schema := fmt.Sprintf("database_migration_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	if _, err := admin.ExecContext(ctx, `CREATE TABLE `+schema+`.app_schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE FUNCTION `+schema+`.delay_migration_ledger_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1); RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE TRIGGER delay_migration_ledger_insert BEFORE INSERT ON `+schema+`.app_schema_migrations FOR EACH ROW EXECUTE FUNCTION `+schema+`.delay_migration_ledger_insert()`); err != nil {
		t.Fatal(err)
	}

	dbURL, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := dbURL.Query()
	query.Set("search_path", schema)
	dbURL.RawQuery = query.Encode()
	db, err := sql.Open("pgx", dbURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(16)

	root := filepath.Join("..", "..", "..", "db")
	const callers = 8
	start := make(chan struct{})
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = ApplyMigrations(ctx, db, root)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("ApplyMigrations caller %d: %v", i, err)
		}
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM app_schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected all three migration files to be recorded, got %d", count)
	}
}
