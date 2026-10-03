package fleet

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// StartSyntheticObservationFeed adds an explicit source of fresh synthetic
// observations for the local demo. Reads never refresh observations. Vehicles
// 001–005 follow the supplied clock; vehicle 006 remains 31 seconds stale.
func StartSyntheticObservationFeed(ctx context.Context, db *sql.DB, clock Clock, interval time.Duration, logger *slog.Logger) {
	if clock == nil {
		clock = time.Now
	}
	if interval < time.Second {
		interval = 10 * time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		refresh := func() {
			if err := refreshSeedObservations(ctx, db, clock().UTC()); err != nil && ctx.Err() == nil {
				logger.Warn("synthetic observation update failed", "error", err)
			}
		}
		refresh()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}

func refreshSeedObservations(ctx context.Context, db *sql.DB, asOf time.Time) error {
	_, err := db.ExecContext(ctx, `UPDATE fleet_vehicles SET observed_at=CASE WHEN vehicle_id='vehicle-006' THEN $1::timestamptz - interval '31 seconds' ELSE $1::timestamptz END WHERE vehicle_id IN ('vehicle-001','vehicle-002','vehicle-003','vehicle-004','vehicle-005','vehicle-006')`, asOf)
	return err
}
