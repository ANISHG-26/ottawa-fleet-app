package fleet

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const SimulationRoute20Profile = "route20synthetic"

// SeedSimulationProfile opts an otherwise untouched six-vehicle demo database
// into the bounded 20-vehicle synthetic route fixture. It never resets or
// changes existing vehicle rows. Call after EnsureSeed during explicit init.
func SeedSimulationProfile(ctx context.Context, db *sql.DB, scenarioClock time.Time, profile string) error {
	if profile != SimulationRoute20Profile {
		return fmt.Errorf("unsupported Fleet simulation profile %q", profile)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('fleet-inventory-profile',0))`); err != nil {
		return err
	}
	var stored string
	err = tx.QueryRowContext(ctx, `SELECT profile FROM fleet_inventory_profile WHERE singleton=true FOR UPDATE`).Scan(&stored)
	if err == nil {
		if stored != profile {
			return fmt.Errorf("Fleet inventory already uses profile %q", stored)
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM fleet_vehicles`).Scan(&count); err != nil {
			return err
		}
		if count != 20 {
			return fmt.Errorf("route20synthetic profile is recorded but inventory has %d vehicles", count)
		}
		return tx.Commit()
	}
	if err != sql.ErrNoRows {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT vehicle_id FROM fleet_vehicles ORDER BY vehicle_id FOR UPDATE`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var reservations, effects, vehicles, known int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM fleet_reservations`).Scan(&reservations); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM fleet_simulation_effects`).Scan(&effects); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE vehicle_id IN ('vehicle-001','vehicle-002','vehicle-003','vehicle-004','vehicle-005','vehicle-006')) FROM fleet_vehicles`).Scan(&vehicles, &known); err != nil {
		return err
	}
	if reservations != 0 || effects != 0 || vehicles != 6 || known != 6 {
		return fmt.Errorf("route20synthetic requires exactly the untouched six-vehicle seed with no reservation or simulation history (vehicles=%d reservations=%d effects=%d)", vehicles, reservations, effects)
	}
	// Keep the six v1 seed rows and their state byte-for-byte unchanged.
	asOf := scenarioClock.UTC()
	zones := simulationProfileZones()
	for i, zone := range zones {
		id := fmt.Sprintf("vehicle-%03d", i+7)
		if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_vehicles(vehicle_id,zone,seats,availability,observed_at,maintenance,battery_percent) VALUES($1,$2,4,'available',$3,false,100)`, id, zone, asOf); err != nil {
			return fmt.Errorf("seed simulation vehicle %s: %w", id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fleet_inventory_profile(singleton,profile,initialized_at) VALUES(true,$1,$2)`, profile, asOf); err != nil {
		return err
	}
	return tx.Commit()
}

func simulationProfileZones() []string {
	return []string{"lansdowne", "lansdowne", "lansdowne", "lansdowne", "lansdowne", "centretown", "centretown", "centretown", "centretown", "centretown", "glebe", "glebe", "byward-market", "byward-market"}
}
