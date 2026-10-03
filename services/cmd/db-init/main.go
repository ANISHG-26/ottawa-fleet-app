package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/fleet"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	migrations := os.Getenv("DB_MIGRATIONS_DIR")
	if migrations == "" {
		migrations = "../db"
	}
	if err := database.ApplyMigrations(ctx, db, migrations); err != nil {
		log.Fatalf("apply migrations: %v", err)
	}
	if _, err := database.EnsureDatasetEpoch(ctx, db); err != nil {
		log.Fatalf("initialize dataset epoch: %v", err)
	}
	seedClock := time.Now().UTC()
	if raw := os.Getenv("FLEET_SEED_TIME"); raw != "" {
		seedClock, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			log.Fatalf("FLEET_SEED_TIME must be RFC3339 UTC: %v", err)
		}
	}
	if err := fleet.EnsureSeed(ctx, db, seedClock); err != nil {
		log.Fatalf("seed fleet: %v", err)
	}
	log.Printf("database initialized; fleet seed clock %s", seedClock.UTC().Format(time.RFC3339))
}
