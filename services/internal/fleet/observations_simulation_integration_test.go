package fleet

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
)

func TestSyntheticObservationFeedRefreshesRoute20ProfileOnly(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; PostgreSQL integration test skipped")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.ApplyMigrations(ctx, db, "../../../db"); err != nil {
		t.Fatal(err)
	}

	var profile sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT profile FROM fleet_inventory_profile WHERE singleton=true`).Scan(&profile); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	if profile.Valid {
		if profile.String != SimulationRoute20Profile {
			t.Fatalf("unexpected inventory profile %q", profile.String)
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM fleet_vehicles`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 6 {
			// The preceding persistence test rebuilds only the six v1 seed rows.
			// Restore the already-recorded synthetic fixture without changing its
			// profile row or any seed vehicle.
			for i, zone := range simulationProfileZones() {
				id := fmt.Sprintf("vehicle-%03d", i+7)
				if _, err := db.ExecContext(ctx, `INSERT INTO fleet_vehicles(vehicle_id,zone,seats,availability,observed_at,maintenance,battery_percent) VALUES($1,$2,4,'available',$3,false,100) ON CONFLICT(vehicle_id) DO NOTHING`, id, zone, base); err != nil {
					t.Fatalf("restore profile vehicle %s: %v", id, err)
				}
			}
		} else if count != 20 {
			t.Fatalf("route20 profile has %d vehicles, want 6 or 20", count)
		}
	} else {
		// The preceding persistence integration test leaves reservation history
		// behind. Clear that dedicated test fixture so the public profile seeder
		// sees its required six seed rows.
		if _, err := db.ExecContext(ctx, `DELETE FROM fleet_simulation_effects; DELETE FROM fleet_reservations; DELETE FROM fleet_vehicles WHERE vehicle_id NOT IN ('vehicle-001','vehicle-002','vehicle-003','vehicle-004','vehicle-005','vehicle-006')`); err != nil {
			t.Fatal(err)
		}
		if err := EnsureSeed(ctx, db, base); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_vehicles(vehicle_id,zone,seats,availability,observed_at,maintenance,battery_percent) VALUES('custom-observation-test','lansdowne',4,'available',$1,false,100)`, base); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_vehicles(vehicle_id,zone,seats,availability,observed_at,maintenance,battery_percent) VALUES('vehicle-010-custom','lansdowne',4,'available',$1,false,100)`, base); err != nil {
		t.Fatal(err)
	}
	stale := base.Add(-time.Minute)
	if _, err := db.ExecContext(ctx, `UPDATE fleet_vehicles SET observed_at=$1`, stale); err != nil {
		t.Fatal(err)
	}
	if err := refreshSeedObservations(ctx, db, base); err != nil {
		t.Fatal(err)
	}
	assertObservationTime(t, ctx, db, "vehicle-001", base)
	assertObservationTime(t, ctx, db, "vehicle-006", base.Add(-31*time.Second))
	assertObservationTime(t, ctx, db, "custom-observation-test", stale)
	assertObservationTime(t, ctx, db, "vehicle-010-custom", stale)

	// Remove the arbitrary test ID before the profile seeder validates the six
	// untouched seed rows.
	if _, err := db.ExecContext(ctx, `DELETE FROM fleet_vehicles WHERE vehicle_id IN ('custom-observation-test','vehicle-010-custom')`); err != nil {
		t.Fatal(err)
	}
	if err := SeedSimulationProfile(ctx, db, base, SimulationRoute20Profile); err != nil {
		t.Fatal(err)
	}
	future := base.Add(45 * time.Second)
	if err := refreshSeedObservations(ctx, db, future); err != nil {
		t.Fatal(err)
	}
	for i := 7; i <= 20; i++ {
		assertObservationTime(t, ctx, db, fmt.Sprintf("vehicle-%03d", i), future)
	}
	assertObservationTime(t, ctx, db, "vehicle-001", future)
	assertObservationTime(t, ctx, db, "vehicle-006", future.Add(-31*time.Second))
}

func assertObservationTime(t *testing.T, ctx context.Context, db *sql.DB, vehicleID string, want time.Time) {
	t.Helper()
	var got time.Time
	if err := db.QueryRowContext(ctx, `SELECT observed_at FROM fleet_vehicles WHERE vehicle_id=$1`, vehicleID).Scan(&got); err != nil {
		t.Fatalf("read observation for %s: %v", vehicleID, err)
	}
	if !got.Equal(want) {
		t.Fatalf("observation for %s = %s, want %s", vehicleID, got, want)
	}
}
