package fleet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSimulationRoutesRejectMalformedIdentities(t *testing.T) {
	h := NewHandler(nil, func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }, FaultConfig{})
	cases := []struct {
		name, method, path string
	}{
		{"event receipt", http.MethodGet, "/v2/fleet/events/NO"},
		{"trip start", http.MethodPost, "/v2/fleet/vehicles/NO/trips/start"},
		{"position", http.MethodGet, "/v2/fleet/vehicles/NO/position"},
		{"position write", http.MethodPut, "/v2/fleet/vehicles/NO/position"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400 for malformed path identity", w.Code, w.Body.String())
			}
		})
	}
}

func TestSimulationProfileKeepsRouteZonesAvailable(t *testing.T) {
	zones := simulationProfileZones()
	if len(zones) != 14 {
		t.Fatalf("additional inventory=%d want 14", len(zones))
	}
	counts := map[string]int{}
	for _, zone := range zones {
		counts[zone]++
	}
	if counts["lansdowne"] != 5 || counts["centretown"] != 5 || counts["glebe"] != 2 || counts["byward-market"] != 2 {
		t.Fatalf("unexpected route profile zones: %#v", counts)
	}
	if err := SeedSimulationProfile(nil, nil, time.Time{}, "unknown"); err == nil {
		t.Fatal("unsupported profile accepted")
	}
}

func TestSimulationFingerprintAndRouteCatalogAreDeterministic(t *testing.T) {
	command := fleetTripCommand{EventID: "event-trip-001", RunID: "run-demo-001", RideID: "ride-demo-001",
		TripID: "trip-demo-001", VehicleID: "vehicle-001", ReservationID: "ride-demo-001",
		RouteID: simulationRouteID, ExpectedVehicleVersion: 4}
	first, err := effectFingerprint(command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := effectFingerprint(command)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("fingerprint not stable: %q %q", first, second)
	}
	command.ExpectedVehicleVersion++
	changed, err := effectFingerprint(command)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changed command reused its fingerprint")
	}
	if got := routePoint(0); got != (SimulationPoint{Latitude: 45.399, Longitude: -75.684}) {
		t.Fatalf("start point=%+v", got)
	}
	if got := routePoint(20); got != (SimulationPoint{Latitude: 45.42, Longitude: -75.693}) {
		t.Fatalf("end point=%+v", got)
	}
	data, err := json.Marshal(routePoint(20))
	if err != nil || string(data) != `{"latitude":45.42,"longitude":-75.693}` {
		t.Fatalf("route point JSON=%s err=%v", data, err)
	}
}
