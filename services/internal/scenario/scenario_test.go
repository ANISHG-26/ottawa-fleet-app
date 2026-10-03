package scenario

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestInputsAreDeterministicAndCapped(t *testing.T) {
	a := Inputs(37, 10, "surge")
	b := Inputs(37, 10, "surge")
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different logical input sequence")
	}
	if len(a) != 10 || a[0].Key == a[1].Key || a[0].Key == "" {
		t.Fatalf("unexpected bounded keys: %+v", a)
	}
	if Inputs(38, 10, "surge")[0].Key == a[0].Key {
		t.Fatal("seed did not scope stable idempotency keys")
	}
	if key := Inputs(1, 10, "surge")[0].Key; key != "surge_0001" {
		t.Fatalf("default surge key should match the contract, got %q", key)
	}
	if Inputs(1, 10, "normal")[0].Key == Inputs(1, 10, "surge")[0].Key {
		t.Fatal("different scenario modes reused an idempotency key")
	}
}

func TestSubmitRetriesWithStableRideIdentity(t *testing.T) {
	calls := 0
	keys := []string{}
	bodies := [][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if calls == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":"temporarily_unavailable"}`))
			return
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"ride_id":"ride-test-001","state":"queued"}`))
	}))
	defer server.Close()
	out := submit(context.Background(), server.Client(), server.URL, "surge_0001", ridePayload{PickupZone: "lansdowne", DropoffZone: "centretown", Passengers: 2})
	if out.RideID != "ride-test-001" || calls != 2 {
		t.Fatalf("unexpected retry result %+v calls=%d", out, calls)
	}
	if keys[0] != "surge_0001" || keys[1] != keys[0] {
		t.Fatalf("retry changed idempotency key: %v", keys)
	}
	if !reflect.DeepEqual(bodies[0], bodies[1]) {
		t.Fatalf("retry changed logical ride request: %s != %s", bodies[0], bodies[1])
	}
}

func TestScenarioBoundsAndLoopbackOnlyTargets(t *testing.T) {
	base := Config{Seed: 1, Count: 10, Interval: time.Second, Observe: 60 * time.Second, Mode: "surge", FleetURL: "http://localhost:8080", RideURL: "http://127.0.0.1:8081"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name   string
		mutate func(*Config)
	}{{"count", func(c *Config) { c.Count = 101 }}, {"duration", func(c *Config) { c.Count = 100; c.Interval = time.Second }}, {"observation", func(c *Config) { c.Observe = 61 * time.Second }}, {"arbitrary host", func(c *Config) { c.RideURL = "http://example.com:8081" }}, {"wrong port", func(c *Config) { c.FleetURL = "http://localhost:8081" }}, {"unbounded latency", func(c *Config) { c.Mode = "fault"; c.FaultLatencyMS = 1001 }}, {"fault opt in", func(c *Config) { c.FaultErrorPercent = 3 }}}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("expected configuration rejection")
			}
		})
	}
}
