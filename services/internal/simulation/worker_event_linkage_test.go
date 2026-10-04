package simulation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestAcceptedRideIdentitySurvivesPreStartAssignmentFailurePostgres(t *testing.T) {
	db := openStopRecoveryDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	runID := fmt.Sprintf("run-event-linkage-%d", now.UnixNano())
	rideID, tripID := testRideID(runID), testTripID(runID)
	insertStopRecoveryRun(t, db, runID, "running", now, false)
	defer deleteStopRecoveryRun(t, db, runID)
	if _, err := db.Exec(`UPDATE simulation_requests SET scheduled_at=$2 WHERE run_id=$1`, runID, now); err != nil {
		t.Fatal(err)
	}

	var startCalls atomic.Int32
	ride := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/rides":
			_ = json.NewEncoder(w).Encode(map[string]string{"ride_id": rideID, "state": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/rides/"+rideID:
			_ = json.NewEncoder(w).Encode(map[string]string{"ride_id": rideID, "state": "failed"})
		case r.Method == http.MethodPost && r.URL.Path == "/v2/rides/"+rideID+"/trip":
			startCalls.Add(1)
			http.Error(w, "trip start must not be issued", http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ride.Close()
	h := NewHandler(db, Config{Enabled: true, RideURL: ride.URL, Clock: func() time.Time { return now }})
	if err := h.advance(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.advance(ctx); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v2/simulation/runs/"+runID+"/events", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list events status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Items []Event `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 2 {
		t.Fatalf("got %d events, want both pre-created events", len(response.Items))
	}
	for _, event := range response.Items {
		if event.RideID != rideID || event.Result != "failed" {
			t.Errorf("event %s has ride_id=%q result=%q, want %q/failed", event.Kind, event.RideID, event.Result, rideID)
		}
		if event.TripID != tripID {
			t.Errorf("event %s trip_id=%q, want %q", event.Kind, event.TripID, tripID)
		}
	}
	if got := startCalls.Load(); got != 0 {
		t.Fatalf("owner received %d trip-start calls for failed assignment", got)
	}
}
