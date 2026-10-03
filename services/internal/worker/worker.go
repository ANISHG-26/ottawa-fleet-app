package worker

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/buildinfo"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type Worker struct {
	Store                 JobStore
	Fleet                 Fleet
	Owner                 string
	Logger                *slog.Logger
	PauseAfterReservation bool
	claimed               atomic.Uint64
	completed             atomic.Uint64
	failed                atomic.Uint64
	retried               atomic.Uint64
	recoveryErrors        atomic.Uint64
}

func (w *Worker) Run(ctx context.Context) error {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	if w.Owner == "" {
		w.Owner = "worker-local"
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := w.Store.Claim(ctx, w.Owner)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.Logger.Error("job claim failed", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				continue
			}
		}
		if job != nil {
			w.claimed.Add(1)
			w.process(ctx, *job)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) process(ctx context.Context, j Job) {
	ctx, span := otel.Tracer("ottawa-fleet/worker").Start(ctx, "assignment.process", trace.WithAttributes(
		attribute.Int("assignment.attempt", j.Attempts),
		attribute.Bool("assignment.reconcile_only", j.ReconcileOnly),
	))
	defer span.End()
	started := time.Now()
	attrs := []any{"request_id", j.RequestID, "ride_id", j.RideID, "job_id", j.ID, "attempt", j.Attempts}
	if sc := span.SpanContext(); sc.IsValid() {
		attrs = append(attrs, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	}
	defer func() {
		w.Logger.Info("assignment job processed", append(attrs, "duration_ms", time.Since(started).Milliseconds())...)
	}()
	get, err := w.Fleet.Get(ctx, j.RideID, j.RequestID)
	if err != nil || transient(get.Status) {
		w.transient(ctx, j, err, get.Status, "reservation_get")
		return
	}
	if j.Invalid {
		w.releaseAndFail(ctx, j, "invalid_job")
		return
	}
	if get.Status == 200 {
		if get.Reservation.State == "reserved" && get.Reservation.VehicleID != "" {
			w.complete(ctx, j, get.Reservation.VehicleID)
			return
		}
		if get.Reservation.State == "released" {
			code := j.PendingFailureCode
			if code == "" {
				code = "invalid_job"
			}
			w.releaseAndFail(ctx, j, code)
			return
		}
		w.releaseAndFail(ctx, j, "invalid_job")
		return
	}
	if get.Status != 404 {
		w.releaseAndFail(ctx, j, "invalid_job")
		return
	}
	if j.ReconcileOnly {
		code := j.PendingFailureCode
		if code == "" {
			code = "retry_exhausted"
		}
		w.releaseAndFail(ctx, j, code)
		return
	}
	post, err := w.Fleet.Reserve(ctx, j, j.RequestID)
	if err != nil || transient(post.Status) {
		w.transient(ctx, j, err, post.Status, "reservation_post")
		return
	}
	if post.Status == 200 || post.Status == 201 {
		if post.Reservation.State != "reserved" || post.Reservation.VehicleID == "" {
			w.releaseAndFail(ctx, j, "invalid_job")
			return
		}
		if w.PauseAfterReservation {
			w.Logger.Warn("test hook paused after reservation; waiting for termination", append(attrs, "vehicle_id", post.Reservation.VehicleID)...)
			<-ctx.Done()
			return
		}
		w.complete(ctx, j, post.Reservation.VehicleID)
		return
	}
	code := "invalid_job"
	if post.Status == 409 && post.Code == "no_capacity" {
		code = "no_capacity"
	}
	w.releaseAndFail(ctx, j, code)
}

func (w *Worker) complete(ctx context.Context, j Job, vehicle string) {
	ok, err := w.Store.Complete(ctx, j, vehicle)
	if err != nil {
		w.Logger.Error("job completion commit failed", "request_id", j.RequestID, "ride_id", j.RideID, "job_id", j.ID, "error", err)
		return
	}
	if !ok {
		w.Logger.Warn("discarded result after lease loss", "request_id", j.RequestID, "ride_id", j.RideID, "job_id", j.ID)
		return
	}
	w.completed.Add(1)
}

func (w *Worker) transient(ctx context.Context, j Job, callErr error, status int, phase string) {
	if callErr != nil {
		w.Logger.Warn("fleet call failed", "request_id", j.RequestID, "ride_id", j.RideID, "job_id", j.ID, "phase", phase, "error", callErr)
	} else {
		w.Logger.Warn("fleet returned transient status", "request_id", j.RequestID, "ride_id", j.RideID, "job_id", j.ID, "phase", phase, "status", status)
	}
	if j.ReconcileOnly {
		w.delayReconcile(ctx, j, j.PendingFailureCode)
		return
	}
	if j.Attempts < 5 {
		delay := time.Duration(1<<(j.Attempts-1)) * time.Second
		ok, err := w.Store.Retry(ctx, j, delay)
		if err != nil {
			w.Logger.Error("retry scheduling failed", "ride_id", j.RideID, "job_id", j.ID, "error", err)
			return
		}
		if ok {
			w.retried.Add(1)
		}
		return
	}
	w.delayReconcile(ctx, j, "")
}

func (w *Worker) releaseAndFail(ctx context.Context, j Job, code string) {
	release, err := w.Fleet.Release(ctx, j.RideID, j.RequestID)
	if err != nil || transient(release.Status) || release.Status < 200 || release.Status >= 300 {
		w.recoveryErrors.Add(1)
		w.Logger.Warn("reservation cleanup pending", "request_id", j.RequestID, "ride_id", j.RideID, "job_id", j.ID, "status", release.Status, "error", err)
		w.delayReconcile(ctx, j, code)
		return
	}
	ok, err := w.Store.Fail(ctx, j, code)
	if err != nil {
		w.Logger.Error("terminal failure commit failed", "ride_id", j.RideID, "job_id", j.ID, "error", err)
		return
	}
	if ok {
		w.failed.Add(1)
	}
}

func (w *Worker) delayReconcile(ctx context.Context, j Job, code string) {
	ok, err := w.Store.DelayReconcile(ctx, j, 8*time.Second, code)
	if err != nil {
		w.recoveryErrors.Add(1)
		w.Logger.Error("reconciliation delay scheduling failed", "request_id", j.RequestID, "ride_id", j.RideID, "job_id", j.ID, "error", err)
		return
	}
	if !ok {
		w.Logger.Warn("lease lost while scheduling reconciliation", "ride_id", j.RideID, "job_id", j.ID)
	}
}

func transient(status int) bool { return status == 0 || status == 500 || status == 503 }

func (w *Worker) Handler(dbReady func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(rw http.ResponseWriter, r *http.Request) {
		httpapi.WriteJSON(rw, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/buildinfo", func(rw http.ResponseWriter, r *http.Request) { httpapi.WriteJSON(rw, 200, buildinfo.Current()) })
	mux.HandleFunc("/readyz", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := dbReady(ctx); err != nil {
			httpapi.WriteError(rw, r, 503, "temporarily_unavailable", "database unavailable")
			return
		}
		httpapi.WriteJSON(rw, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/metrics", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = rw.Write([]byte("assignment_claimed_total " + itoa(w.claimed.Load()) + "\nassignment_completed_total " + itoa(w.completed.Load()) + "\nassignment_failed_total " + itoa(w.failed.Load()) + "\nassignment_retried_total " + itoa(w.retried.Load()) + "\nassignment_recovery_errors_total " + itoa(w.recoveryErrors.Load()) + "\n"))
	})
	return httpapi.RequestID(mux)
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
