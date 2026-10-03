package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func DatasetEpoch(ctx context.Context, db *sql.DB) (string, error) {
	var epoch string
	err := db.QueryRowContext(ctx, `SELECT dataset_epoch FROM app_runtime_state WHERE singleton=1`).Scan(&epoch)
	return epoch, err
}

// EnsureDatasetEpoch creates a durable epoch on first initialization. It is
// retained across API restarts so cursors remain valid for one dataset lifetime.
func EnsureDatasetEpoch(ctx context.Context, db *sql.DB) (string, error) {
	epoch, err := DatasetEpoch(ctx, db)
	if err == nil {
		return epoch, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	candidate := hex.EncodeToString(bytes[:])
	if _, err := db.ExecContext(ctx, `INSERT INTO app_runtime_state(singleton,dataset_epoch) VALUES(1,$1) ON CONFLICT(singleton) DO NOTHING`, candidate); err != nil {
		return "", err
	}
	return DatasetEpoch(ctx, db)
}

// RotateDatasetEpoch invalidates every existing cursor after an explicit local
// reset that keeps the database itself alive.
func RotateDatasetEpoch(ctx context.Context, db *sql.DB) (string, error) {
	if _, err := EnsureDatasetEpoch(ctx, db); err != nil {
		return "", err
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	candidate := hex.EncodeToString(bytes[:])
	_, err := db.ExecContext(ctx, `UPDATE app_runtime_state SET dataset_epoch=$1,updated_at=clock_timestamp() WHERE singleton=1`, candidate)
	if err != nil {
		return "", err
	}
	return candidate, nil
}

func Open(ctx context.Context, databaseURL string) (*sql.DB, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ApplyMigrations applies ordered SQL files in db/fleet and db/ride. Each file
// is committed atomically and tracked so db-init can safely be repeated.
// This application-scoped key serializes schema initialization across every
// process using the shared PostgreSQL database.
const migrationLockKey int64 = 0x4f5454415741464c // "OTTAWFL"

func ApplyMigrations(ctx context.Context, db *sql.DB, root string) (retErr error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get migration connection: %w", err)
	}
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		discardConn(conn)
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationLockKey).Scan(&unlocked)
		if unlockErr == nil && !unlocked {
			unlockErr = errors.New("migration lock was not held by its connection")
		}
		if unlockErr != nil {
			discardConn(conn)
			if retErr == nil {
				retErr = fmt.Errorf("release migration lock: %w", unlockErr)
			}
			return
		}
		_ = conn.Close()
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	for _, owner := range []string{"fleet", "ride"} {
		files, err := filepath.Glob(filepath.Join(root, owner, "*.sql"))
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("no %s migrations found under %s", owner, filepath.Join(root, owner))
		}
		sort.Strings(files)
		for _, path := range files {
			name := owner + "/" + filepath.Base(path)
			var applied bool
			if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_schema_migrations WHERE name=$1)`, name).Scan(&applied); err != nil {
				return err
			}
			if applied {
				continue
			}
			sqlBytes, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, string(sqlBytes)); err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO app_schema_migrations(name) VALUES($1)`, name)
			}
			if err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
			if err = tx.Commit(); err != nil {
				return fmt.Errorf("commit migration %s: %w", name, err)
			}
		}
	}
	return nil
}

// discardConn prevents a session-level advisory lock from returning to the
// pool if an unlock fails or its independent cleanup deadline expires.
func discardConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
