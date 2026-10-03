package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
)

func TestPostgresFleetReservationPersistenceAndConcurrency(t *testing.T) {
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
	root := filepath.Clean(filepath.Join("..", "..", "..", "db"))
	if err := database.ApplyMigrations(ctx, db, root); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplyMigrations(ctx, db, root); err != nil {
		t.Fatalf("repeat migration run: %v", err)
	}
	epoch, err := database.EnsureDatasetEpoch(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	cursor := httpapi.EncodeCursor("fleet:"+epoch, 2, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), "vehicle-002")
	newEpoch, err := database.RotateDatasetEpoch(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if newEpoch == epoch {
		t.Fatal("dataset reset did not rotate epoch")
	}
	if _, _, err := httpapi.DecodeCursor(cursor, "fleet:"+newEpoch, 2); err == nil {
		t.Fatal("cursor remained valid after dataset epoch rotation")
	}
	if _, err := database.RotateDatasetEpoch(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fleet_reservations; DELETE FROM fleet_vehicles`); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	if err := EnsureSeed(ctx, db, clock); err != nil {
		t.Fatal(err)
	}
	h := httpapi.RequestID(NewHandler(db, func() time.Time { return clock }, FaultConfig{}))
	var observedBefore time.Time
	if err := db.QueryRowContext(ctx, `SELECT observed_at FROM fleet_vehicles WHERE vehicle_id='vehicle-001'`).Scan(&observedBefore); err != nil {
		t.Fatal(err)
	}
	listReq := httptest.NewRequest(http.MethodGet, "/v1/fleet", nil)
	listW := httptest.NewRecorder()
	h.ServeHTTP(listW, listReq)
	if listW.Code != 200 {
		t.Fatalf("fleet list status=%d body=%s", listW.Code, listW.Body.String())
	}
	var page FleetPage
	if err := json.Unmarshal(listW.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	available, unknown := 0, 0
	for _, v := range page.Items {
		if v.Availability == "available" {
			available++
		}
		if v.Availability == "unknown" {
			unknown++
		}
	}
	if available != 5 || unknown != 1 {
		t.Fatalf("seed availability: available=%d unknown=%d", available, unknown)
	}
	var observedAfterRead time.Time
	if err := db.QueryRowContext(ctx, `SELECT observed_at FROM fleet_vehicles WHERE vehicle_id='vehicle-001'`).Scan(&observedAfterRead); err != nil {
		t.Fatal(err)
	}
	if !observedAfterRead.Equal(observedBefore) {
		t.Fatalf("GET refreshed telemetry: before=%s after=%s", observedBefore, observedAfterRead)
	}
	clock = clock.Add(time.Minute)
	if err := refreshSeedObservations(ctx, db, clock); err != nil {
		t.Fatal(err)
	}
	feedReq := httptest.NewRequest(http.MethodGet, "/v1/fleet", nil)
	feedW := httptest.NewRecorder()
	h.ServeHTTP(feedW, feedReq)
	var refreshed FleetPage
	if err := json.Unmarshal(feedW.Body.Bytes(), &refreshed); err != nil {
		t.Fatal(err)
	}
	available, unknown = 0, 0
	for _, v := range refreshed.Items {
		if v.Availability == "available" {
			available++
		}
		if v.Availability == "unknown" {
			unknown++
		}
	}
	if available != 5 || unknown != 1 {
		t.Fatalf("synthetic feed availability: available=%d unknown=%d", available, unknown)
	}
	// Maintenance and low battery disqualify fresh inventory from reservations.
	if _, err := db.ExecContext(ctx, `UPDATE fleet_vehicles SET maintenance=true WHERE vehicle_id='vehicle-001'; UPDATE fleet_vehicles SET battery_percent=19 WHERE vehicle_id='vehicle-002'`); err != nil {
		t.Fatal(err)
	}
	blockedBody, _ := json.Marshal(ReserveRequest{RideID: "blocked-ride", PickupZone: "lansdowne", Passengers: 1})
	blockedReq := httptest.NewRequest(http.MethodPost, "/v1/reservations", bytes.NewReader(blockedBody))
	blockedReq.Header.Set("Content-Type", "application/json")
	blockedW := httptest.NewRecorder()
	h.ServeHTTP(blockedW, blockedReq)
	if blockedW.Code != 409 {
		t.Fatalf("maintenance/low battery were reservable: status=%d", blockedW.Code)
	}
	if _, err := db.ExecContext(ctx, `UPDATE fleet_vehicles SET maintenance=false,battery_percent=100 WHERE vehicle_id IN ('vehicle-001','vehicle-002')`); err != nil {
		t.Fatal(err)
	}
	staleBody, _ := json.Marshal(ReserveRequest{RideID: "stale-ride", PickupZone: "byward-market", Passengers: 1})
	staleReq := httptest.NewRequest(http.MethodPost, "/v1/reservations", bytes.NewReader(staleBody))
	staleReq.Header.Set("Content-Type", "application/json")
	staleW := httptest.NewRecorder()
	h.ServeHTTP(staleW, staleReq)
	if staleW.Code != 409 {
		t.Fatalf("stale vehicle was reservable: status=%d", staleW.Code)
	}
	var mu sync.Mutex
	statuses := []int{}
	vehicles := []string{}
	createdRides := []string{}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			rideID := string(rune('a'+i)) + "-ride"
			body, _ := json.Marshal(ReserveRequest{RideID: rideID, PickupZone: "lansdowne", Passengers: 2})
			req := httptest.NewRequest(http.MethodPost, "/v1/reservations", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			mu.Lock()
			defer mu.Unlock()
			statuses = append(statuses, w.Code)
			if w.Code == 201 {
				createdRides = append(createdRides, rideID)
				var got Reservation
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Error(err)
				} else if got.VehicleID != nil {
					vehicles = append(vehicles, *got.VehicleID)
				}
			}
		}()
	}
	wg.Wait()
	if len(statuses) != 3 {
		t.Fatalf("statuses: %v", statuses)
	}
	created, conflict := 0, 0
	for _, status := range statuses {
		if status == 201 {
			created++
		}
		if status == 409 {
			conflict++
		}
	}
	if created != 2 || conflict != 1 {
		t.Fatalf("statuses=%v, want two reservations and one no_capacity", statuses)
	}
	if len(vehicles) != 2 || vehicles[0] == vehicles[1] {
		t.Fatalf("vehicles not unique: %v", vehicles)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(ctx, url)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()
	// Replaying the durable ride identity after a new handler simulates restart.
	replayBody, _ := json.Marshal(ReserveRequest{RideID: createdRides[0], PickupZone: "lansdowne", Passengers: 2})
	req := httptest.NewRequest(http.MethodPost, "/v1/reservations", bytes.NewReader(replayBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	httpapi.RequestID(NewHandler(db, func() time.Time { return clock }, FaultConfig{})).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("replay status=%d body=%s", w.Code, w.Body.String())
	}
	var replay Reservation
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.VehicleID == nil {
		t.Fatalf("replay lost vehicle: %#v", replay)
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/reservations/"+createdRides[0], nil)
	deleteW := httptest.NewRecorder()
	httpapi.RequestID(NewHandler(db, func() time.Time { return clock }, FaultConfig{})).ServeHTTP(deleteW, deleteReq)
	if deleteW.Code != 200 {
		t.Fatalf("release status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}
	postAgain := httptest.NewRequest(http.MethodPost, "/v1/reservations", bytes.NewReader(replayBody))
	postAgain.Header.Set("Content-Type", "application/json")
	postW := httptest.NewRecorder()
	httpapi.RequestID(NewHandler(db, func() time.Time { return clock }, FaultConfig{})).ServeHTTP(postW, postAgain)
	if postW.Code != 409 {
		t.Fatalf("released ride could be reserved again: status=%d body=%s", postW.Code, postW.Body.String())
	}
	missingRelease := httptest.NewRequest(http.MethodDelete, "/v1/reservations/no-reservation", nil)
	missingW := httptest.NewRecorder()
	httpapi.RequestID(NewHandler(db, func() time.Time { return clock }, FaultConfig{})).ServeHTTP(missingW, missingRelease)
	if missingW.Code != 200 {
		t.Fatalf("tombstone status=%d", missingW.Code)
	}
	var tombstone Reservation
	if err := json.Unmarshal(missingW.Body.Bytes(), &tombstone); err != nil {
		t.Fatal(err)
	}
	if tombstone.VehicleID != nil || tombstone.ReservedAt != nil || tombstone.State != "released" {
		t.Fatalf("invalid tombstone: %#v", tombstone)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}
