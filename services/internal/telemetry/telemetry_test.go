package telemetry

import (
	"bytes"
	"context"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestOptionalExporterOffStillPropagatesW3CTraceContext(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	runtime, err := Init(context.Background(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })

	const incoming = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	var outbound string
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	collector := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		outbound = req.Header.Get("traceparent")
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	})
	var clientErr error
	handler := HTTPMiddleware("ride-api")(httpapi.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse("http://collector/v1/traces")
		out, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, u.String(), nil)
		_, clientErr = (&http.Client{Transport: NewHTTPTransport("ride-api", collector)}).Do(out)
		w.WriteHeader(http.StatusAccepted)
	})))
	req, _ := http.NewRequest(http.MethodGet, "http://service/v1/rides/ride-private-id?name=private", nil)
	req.Header.Set("traceparent", incoming)
	req.Header.Set("X-Request-ID", "req-test-123")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d", res.Code)
	}
	if got := strings.Split(outbound, "-"); len(got) != 4 || got[1] != "4bf92f3577b34da6a3ce929d0e0e4736" || got[2] == "00f067aa0ba902b7" || got[3] != "01" {
		t.Fatalf("outbound traceparent did not preserve trace and create a child span: %q", outbound)
	}
	line := logs.String()
	if !strings.Contains(line, `"request_id":"req-test-123"`) || !strings.Contains(line, `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`) || !strings.Contains(line, `"route":"GET /v1/rides/{ride_id}"`) {
		t.Fatalf("completion log lacks bounded trace/request correlation: %s", line)
	}
	if strings.Contains(line, "ride-private-id") || strings.Contains(line, "name=private") {
		t.Fatalf("completion log exposed raw path or query: %s", line)
	}
}

func TestInvalidOTLPEndpointDoesNotFailApplicationInitialization(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://[::1")
	runtime, err := Init(context.Background(), "worker")
	if err != nil {
		t.Fatalf("optional telemetry configuration failed application initialization: %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown telemetry provider: %v", err)
	}
}

func TestEnabledOTLPExporterSendsEndedSpan(t *testing.T) {
	var receivedPath atomic.Value
	var receivedBytes atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read OTLP request: %v", err)
		}
		receivedPath.Store(r.URL.Path)
		receivedBytes.Store(int64(len(body)))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	runtime, err := Init(context.Background(), "telemetry-test")
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("telemetry-test").Start(context.Background(), "bounded.operation")
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := runtime.Shutdown(ctx); err != nil {
		t.Fatalf("flush OTLP span to in-process collector: %v", err)
	}
	if got := receivedPath.Load(); got != "/v1/traces" {
		t.Fatalf("OTLP request path = %v, want /v1/traces", got)
	}
	if got := receivedBytes.Load(); got == 0 {
		t.Fatal("OTLP collector received an empty span payload")
	}
}

func TestUnavailableCollectorDoesNotFailStartupOrOutboundCall(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)

	runtime, err := Init(context.Background(), "telemetry-test")
	if err != nil {
		t.Fatalf("unavailable optional collector failed app startup: %v", err)
	}
	_, span := otel.Tracer("telemetry-test").Start(context.Background(), "bounded.operation")
	span.End()
	var outbound bool
	transport := NewHTTPTransport("ride-api", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		outbound = true
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	}))
	req, _ := http.NewRequest(http.MethodGet, "http://fleet-api/v1/fleet", nil)
	if _, err := (&http.Client{Transport: transport}).Do(req); err != nil {
		t.Fatalf("outbound call was affected by collector availability: %v", err)
	}
	if !outbound {
		t.Fatal("outbound transport was not called")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Export errors are intentionally best-effort. Shutdown can report the
	// expected collector failure, but it must complete within the export bound.
	_ = runtime.Shutdown(ctx)
}

func TestLogFileWriterCapsBytes(t *testing.T) {
	var dst bytes.Buffer
	w := &cappedWriter{writer: &dst, remaining: 5}
	if _, err := w.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("67")); err != nil {
		t.Fatal(err)
	}
	if got := dst.String(); got != "12345" {
		t.Fatalf("capped output = %q", got)
	}
}

func TestRouteNamesDoNotContainEntityIDsOrQuery(t *testing.T) {
	got := routeName(http.MethodGet, "/v1/rides/ride-private-id")
	if got != "GET /v1/rides/{ride_id}" {
		t.Fatalf("route = %q", got)
	}
}
