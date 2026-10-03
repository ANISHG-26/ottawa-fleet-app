package fleet

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// EnsureSeed inserts the deterministic six-vehicle inventory once. The caller
// supplies the scenario clock so fixture time is never reused as live telemetry.
func EnsureSeed(ctx context.Context, db *sql.DB, scenarioClock time.Time) error {
	asOf := scenarioClock.UTC()
	vehicles := []struct {
		id, zone string
		observed time.Time
	}{
		{"vehicle-001", "lansdowne", asOf}, {"vehicle-002", "lansdowne", asOf},
		{"vehicle-003", "centretown", asOf}, {"vehicle-004", "centretown", asOf},
		{"vehicle-005", "glebe", asOf}, {"vehicle-006", "byward-market", asOf.Add(-31 * time.Second)},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range vehicles {
		if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_vehicles(vehicle_id,zone,seats,availability,observed_at,maintenance,battery_percent) VALUES($1,$2,4,'available',$3,false,100) ON CONFLICT(vehicle_id) DO NOTHING`, v.id, v.zone, v.observed); err != nil {
			return fmt.Errorf("seed %s: %w", v.id, err)
		}
	}
	return tx.Commit()
}
