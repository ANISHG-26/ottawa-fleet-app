package fleet

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEffectiveAvailabilityUsesInclusiveThirtySecondWindow(t *testing.T) {
	asOf := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		observed    time.Time
		maintenance bool
		battery     int
		active      bool
		want        string
	}{
		{"fresh", asOf, false, 100, false, "available"},
		{"boundary", asOf.Add(-30 * time.Second), false, 100, false, "available"},
		{"stale", asOf.Add(-31 * time.Second), false, 100, false, "unknown"},
		{"future", asOf.Add(time.Second), false, 100, false, "unknown"},
		{"maintenance", asOf, true, 100, false, "unavailable"},
		{"low battery", asOf, false, 19, false, "unavailable"},
		{"active reservation", asOf, false, 100, true, "reserved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveAvailability("available", tc.observed, tc.maintenance, tc.battery, tc.active, asOf); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestFaultControlsRequireOptInAndLoopback(t *testing.T) {
	request := func(h *Handler, remote, body string) int {
		r := httptest.NewRequest(http.MethodPost, "/debug/faults", bytes.NewBufferString(body))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request(NewHandler(nil, nil, FaultConfig{}), "127.0.0.1:1234", `{"latency_ms":1000,"error_percent":50}`); got != 404 {
		t.Fatalf("disabled controls status=%d", got)
	}
	h := NewHandler(nil, nil, FaultConfig{ControlEnabled: true})
	if got := request(h, "192.0.2.4:1234", `{"latency_ms":1000,"error_percent":50}`); got != 404 {
		t.Fatalf("non-loopback controls status=%d", got)
	}
	if got := request(h, "127.0.0.1:1234", `{"latency_ms":1001,"error_percent":0}`); got != 400 {
		t.Fatalf("unbounded fault status=%d", got)
	}
	if got := request(h, "127.0.0.1:1234", `{"latency_ms":25,"error_percent":10}`); got != 200 {
		t.Fatalf("enabled controls status=%d", got)
	}
	if h.faults.Latency != 25*time.Millisecond || h.faults.ErrorPercent != 10 {
		t.Fatalf("fault config not applied: %#v", h.faults)
	}
	r := httptest.NewRequest(http.MethodPost, "/debug/faults/reset", nil)
	r.RemoteAddr = "[::1]:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || h.faults.Latency != 0 || h.faults.ErrorPercent != 0 {
		t.Fatalf("reset status=%d config=%#v", w.Code, h.faults)
	}
}
