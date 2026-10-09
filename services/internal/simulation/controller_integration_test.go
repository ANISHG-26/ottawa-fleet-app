package simulation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
)

func TestRunIdempotencyConflictActiveAndStopPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.ApplyMigrations(ctx, db, filepath.Join("..", "..", "..", "db")); err != nil {
		t.Fatal(err)
	}
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := make([]map[string]string, 20)
		for i := range items {
			items[i] = map[string]string{"vehicle_id": fmt.Sprintf("vehicle-%03d", i+1)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": nil})
	}))
	defer fleet.Close()
	clock := time.Now().UTC()
	h := NewHandler(db, Config{Enabled: true, FleetURL: fleet.URL, RideURL: "http://127.0.0.1:1", Clock: func() time.Time { return clock }})
	m := Manifest{ScenarioID: "route20synthetic", FleetProfile: "route20synthetic", IdempotencyKey: "integration-run-" + fmt.Sprint(time.Now().UnixNano()), EventRatePerSecond: 2, RequestCount: 2, ExecutionEventCount: 4, DurationSeconds: 60, MaxInFlight: 2, RouteDurationSeconds: 8, RouteID: routeID, StartZone: "lansdowne", EndZone: "centretown", Seed: 4}
	call := func(method, path string, v any) *httptest.ResponseRecorder {
		var b bytes.Buffer
		if v != nil {
			_ = json.NewEncoder(&b).Encode(v)
		}
		r := httptest.NewRequest(method, path, &b)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Handler().ServeHTTP(w, r)
		return w
	}
	request := map[string]any{"manifest": m}
	created := call(http.MethodPost, "/v2/simulation/runs", request)
	if created.Code != 200 {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var first Run
	if err = json.Unmarshal(created.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM simulation_events WHERE run_id=$1`, first.RunID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM simulation_requests WHERE run_id=$1`, first.RunID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM simulation_runs WHERE run_id=$1`, first.RunID)
	}()
	replay := call(http.MethodPost, "/v2/simulation/runs", request)
	var same Run
	if replay.Code != 200 || json.Unmarshal(replay.Body.Bytes(), &same) != nil || same.RunID != first.RunID {
		t.Fatalf("exact replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	changed := m
	changed.Seed++
	if conflict := call(http.MethodPost, "/v2/simulation/runs", map[string]any{"manifest": changed}); conflict.Code != 409 {
		t.Fatalf("changed replay status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	stopped := call(http.MethodDelete, "/v2/simulation/runs/"+first.RunID, map[string]bool{"drain": true})
	if stopped.Code != 200 {
		t.Fatalf("stop status=%d body=%s", stopped.Code, stopped.Body.String())
	}
	var status Run
	if err = json.Unmarshal(stopped.Body.Bytes(), &status); err != nil || status.State != "stopping" || status.DrainDeadlineAt.IsZero() {
		t.Fatalf("stop did not preserve drain: %+v err=%v", status, err)
	}
}
