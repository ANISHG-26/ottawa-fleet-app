package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const maxLogFileBytes = 8 << 20

// Runtime owns the optional trace exporter. OTLP is disabled unless an
// explicit OTEL_EXPORTER_OTLP_ENDPOINT is supplied.
type Runtime struct {
	provider *sdktrace.TracerProvider
}

func Init(ctx context.Context, service string) (*Runtime, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", service)),
	)
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "telemetry trace export disabled: create OTLP exporter: %v\n", err)
		} else {
			opts = append(opts, sdktrace.WithBatcher(exporter,
				sdktrace.WithMaxQueueSize(256),
				sdktrace.WithMaxExportBatchSize(64),
				sdktrace.WithBatchTimeout(time.Second),
				sdktrace.WithExportTimeout(2*time.Second),
			))
		}
	}
	provider := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(provider)
	return &Runtime{provider: provider}, nil
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.provider == nil {
		return nil
	}
	return r.provider.Shutdown(ctx)
}

// NewLogger keeps the existing JSON stdout stream and optionally mirrors it to
// a capped JSONL file for the local Alloy file reader. No Docker socket access
// or unbounded host log file is required.
func NewLogger(service string) (*slog.Logger, io.Closer, error) {
	var sinks []io.Writer
	sinks = append(sinks, os.Stdout)
	var closer io.Closer = io.NopCloser(strings.NewReader(""))
	if path := strings.TrimSpace(os.Getenv("LOG_FILE")); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "telemetry file logging disabled: create log directory: %v\n", err)
			return jsonLogger(service, sinks), closer, nil
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "telemetry file logging disabled: open log file: %v\n", err)
			return jsonLogger(service, sinks), closer, nil
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			fmt.Fprintf(os.Stderr, "telemetry file logging disabled: inspect log file: %v\n", err)
			return jsonLogger(service, sinks), closer, nil
		}
		remaining := int64(maxLogFileBytes) - info.Size()
		if remaining < 0 {
			remaining = 0
		}
		sinks = append(sinks, &cappedWriter{writer: file, remaining: remaining})
		closer = file
	}
	return jsonLogger(service, sinks), closer, nil
}

func jsonLogger(service string, sinks []io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.MultiWriter(sinks...), &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", service)
}

type cappedWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	remaining int64
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.remaining <= 0 {
		return len(p), nil
	}
	if int64(len(p)) > w.remaining {
		return len(p), nil
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	return n, nil
}

func HTTPMiddleware(service string) func(http.Handler) http.Handler {
	tracer := otel.Tracer("ottawa-fleet/http")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			route := routeName(r.Method, r.URL.Path)
			ctx, span := tracer.Start(ctx, route, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
				attribute.String("service.name", service),
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
			))
			defer span.End()
			started := time.Now()
			status := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(status, r.WithContext(ctx))
			responseStatus := status.status
			if responseStatus == 0 {
				responseStatus = http.StatusOK
			}
			span.SetAttributes(attribute.Int("http.response.status_code", responseStatus))
			if responseStatus >= 500 {
				span.SetStatus(codes.Error, "server response")
			}
			sc := span.SpanContext()
			attrs := []any{
				"service", service,
				"request_id", status.Header().Get("X-Request-ID"),
				"method", r.Method,
				"route", route,
				"status", responseStatus,
				"duration_ms", time.Since(started).Milliseconds(),
			}
			if sc.IsValid() {
				attrs = append(attrs, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
			}
			slog.Default().Info("http request completed", attrs...)
		})
	}
}

func routeName(method, path string) string {
	switch {
	case path == "/healthz", path == "/readyz", path == "/buildinfo", path == "/metrics":
		return method + " " + path
	case path == "/v1/fleet":
		return method + " /v1/fleet"
	case strings.HasPrefix(path, "/v1/fleet/"):
		return method + " /v1/fleet/{vehicle_id}"
	case path == "/v1/reservations":
		return method + " /v1/reservations"
	case strings.HasPrefix(path, "/v1/reservations/"):
		return method + " /v1/reservations/{ride_id}"
	case path == "/v1/rides":
		return method + " /v1/rides"
	case strings.HasPrefix(path, "/v1/rides/"):
		return method + " /v1/rides/{ride_id}"
	case strings.HasPrefix(path, "/debug/faults"):
		return method + " /debug/faults"
	default:
		return method + " /unmatched"
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func NewHTTPTransport(service string, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		route := "fleet." + req.Method
		ctx, span := otel.Tracer("ottawa-fleet/http-client").Start(req.Context(), route, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			attribute.String("service.name", service),
			attribute.String("http.request.method", req.Method),
			attribute.String("server.address", "fleet-api"),
		))
		out := req.Clone(ctx)
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(out.Header))
		resp, err := base.RoundTrip(out)
		if err != nil {
			span.SetStatus(codes.Error, "request failed")
			span.End()
			return resp, err
		}
		span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
		if resp.StatusCode >= 500 {
			span.SetStatus(codes.Error, "server response")
		}
		span.End()
		return resp, nil
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
