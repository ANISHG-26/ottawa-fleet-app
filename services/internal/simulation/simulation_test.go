package simulation

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManifestBoundsAreIndependentAndRequireRouteEvents(t *testing.T) {
	m := Manifest{ScenarioID: "route20synthetic", FleetProfile: "route20synthetic", IdempotencyKey: "run-demo-001", EventRatePerSecond: 4,
		RequestCount: 20, ExecutionEventCount: 40, DurationSeconds: 60, MaxInFlight: 4, RouteDurationSeconds: 8, RouteID: routeID,
		StartZone: "lansdowne", EndZone: "centretown", Seed: 1}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid bounded manifest: %v", err)
	}
	m.ExecutionEventCount = 39
	if err := m.Validate(); err == nil {
		t.Fatal("manifest admitted fewer events than required starts and completions")
	}
	m.ExecutionEventCount = 40
	m.RequestCount = 21
	if err := m.Validate(); err == nil {
		t.Fatal("manifest admitted more than 20 requests")
	}
}

func TestDisabledControllerRejectsWritesBeforeDatabase(t *testing.T) {
	h := NewHandler(nil, Config{Enabled: false})
	r := httptest.NewRequest(http.MethodPost, "/v2/simulation/runs", strings.NewReader(`{"manifest":{}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
