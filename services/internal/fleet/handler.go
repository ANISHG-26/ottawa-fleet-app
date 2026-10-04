package fleet

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/buildinfo"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
)

var entityID = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)
var zoneSet = map[string]bool{"centretown": true, "glebe": true, "lansdowne": true, "byward-market": true}

type Clock func() time.Time
type FaultConfig struct {
	Latency        time.Duration
	ErrorPercent   int
	ControlEnabled bool
}
type Handler struct {
	db              *sql.DB
	clock           Clock
	faults          FaultConfig
	faultMu         sync.RWMutex
	requests        atomic.Uint64
	failures        atomic.Uint64
	durationNanos   atomic.Int64
	durationBuckets [6]atomic.Uint64
}

func NewHandler(db *sql.DB, clock Clock, faults FaultConfig) *Handler {
	if clock == nil {
		clock = time.Now
	}
	if faults.Latency < 0 {
		faults.Latency = 0
	}
	if faults.Latency > time.Second {
		faults.Latency = time.Second
	}
	if faults.ErrorPercent < 0 {
		faults.ErrorPercent = 0
	}
	if faults.ErrorPercent > 50 {
		faults.ErrorPercent = 50
	}
	return &Handler{db: db, clock: clock, faults: faults}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status := 200
	wrapped := &statusWriter{ResponseWriter: w, status: 200}
	defer func() {
		h.requests.Add(1)
		status = wrapped.status
		elapsed := time.Since(start)
		h.durationNanos.Add(elapsed.Nanoseconds())
		for i, bound := range [...]time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 500 * time.Millisecond, time.Second} {
			if elapsed <= bound {
				h.durationBuckets[i].Add(1)
			}
		}
		slog.Info("http request", "request_id", httpapi.RequestIDFromContext(r.Context()), "method", r.Method, "status", status, "duration_ms", elapsed.Milliseconds())
	}()
	if strings.HasPrefix(r.URL.Path, "/debug/faults") {
		h.routeFaults(wrapped, r)
		return
	}
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" || r.URL.Path == "/buildinfo" {
		h.route(wrapped, r)
		return
	}
	h.faultMu.RLock()
	faults := h.faults
	h.faultMu.RUnlock()
	if faults.Latency > 0 {
		timer := time.NewTimer(faults.Latency)
		select {
		case <-r.Context().Done():
			timer.Stop()
			httpapi.WriteError(wrapped, r, 503, "temporarily_unavailable", "request canceled")
			return
		case <-timer.C:
		}
	}
	if faults.ErrorPercent > 0 && rand.IntN(100) < faults.ErrorPercent {
		h.failures.Add(1)
		httpapi.WriteError(wrapped, r, 503, "temporarily_unavailable", "injected fleet API outage")
		return
	}
	h.route(wrapped, r)
}

type faultRequest struct {
	LatencyMS    int `json:"latency_ms"`
	ErrorPercent int `json:"error_percent"`
}

func (h *Handler) routeFaults(w http.ResponseWriter, r *http.Request) {
	if !h.faults.ControlEnabled || !isLoopback(r.RemoteAddr) {
		httpapi.WriteError(w, r, 404, "not_found", "resource not found")
		return
	}
	if r.Method != http.MethodPost {
		httpapi.WriteError(w, r, 404, "not_found", "resource not found")
		return
	}
	if r.URL.Path == "/debug/faults/reset" {
		h.faultMu.Lock()
		h.faults.Latency = 0
		h.faults.ErrorPercent = 0
		h.faultMu.Unlock()
		httpapi.WriteJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path != "/debug/faults" {
		httpapi.WriteError(w, r, 404, "not_found", "resource not found")
		return
	}
	var request faultRequest
	if p := httpapi.DecodeStrict(w, r, &request); p != nil {
		writeProblem(w, r, p)
		return
	}
	if request.LatencyMS < 0 || request.LatencyMS > 1000 || request.ErrorPercent < 0 || request.ErrorPercent > 50 {
		httpapi.WriteError(w, r, 400, "invalid_request", "latency_ms must be 0..1000 and error_percent 0..50")
		return
	}
	h.faultMu.Lock()
	h.faults.Latency = time.Duration(request.LatencyMS) * time.Millisecond
	h.faults.ErrorPercent = request.ErrorPercent
	h.faultMu.Unlock()
	httpapi.WriteJSON(w, 200, map[string]string{"status": "ok"})
}
func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v2/fleet/") && h.routeSimulation(w, r) {
		return
	}
	if r.URL.Path == "/healthz" {
		httpapi.WriteJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/buildinfo" {
		httpapi.WriteJSON(w, 200, buildinfo.Current())
		return
	}
	if r.URL.Path == "/readyz" {
		if err := h.db.PingContext(r.Context()); err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "database is unavailable")
			return
		}
		if _, err := h.db.ExecContext(r.Context(), `SELECT vehicle_id,zone,seats,availability,observed_at,maintenance,battery_percent FROM fleet_vehicles LIMIT 0`); err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "fleet schema is unavailable")
			return
		}
		if _, err := h.db.ExecContext(r.Context(), `SELECT ride_id,vehicle_id,state,reserved_at,updated_at,pickup_zone,passengers FROM fleet_reservations LIMIT 0`); err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "fleet schema is unavailable")
			return
		}
		if _, err := h.db.ExecContext(r.Context(), `SELECT vehicle_version,operational_state,active_trip_id,position_latitude,position_longitude FROM fleet_vehicles LIMIT 0`); err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "Fleet simulation schema is unavailable")
			return
		}
		if _, err := h.db.ExecContext(r.Context(), `SELECT event_id,effect,payload_fingerprint,receipt FROM fleet_simulation_effects LIMIT 0`); err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "Fleet simulation schema is unavailable")
			return
		}
		if _, err := database.DatasetEpoch(r.Context(), h.db); err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "dataset metadata is unavailable")
			return
		}
		httpapi.WriteJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/metrics" {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		count := h.requests.Load()
		_, _ = fmt.Fprintf(w, "fleet_http_requests_total %d\nfleet_injected_failures_total %d\n# TYPE fleet_http_request_duration_seconds histogram\n", count, h.failures.Load())
		for i, label := range [...]string{"0.005", "0.01", "0.05", "0.1", "0.5", "1"} {
			_, _ = fmt.Fprintf(w, "fleet_http_request_duration_seconds_bucket{le=\"%s\"} %d\n", label, h.durationBuckets[i].Load())
		}
		_, _ = fmt.Fprintf(w, "fleet_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\nfleet_http_request_duration_seconds_sum %.9f\nfleet_http_request_duration_seconds_count %d\n", count, float64(h.durationNanos.Load())/float64(time.Second), count)
		return
	}
	if r.URL.Path == "/v1/fleet" && r.Method == http.MethodGet {
		h.list(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/fleet/") && r.Method == http.MethodGet {
		h.getVehicle(w, r, strings.TrimPrefix(r.URL.Path, "/v1/fleet/"))
		return
	}
	if r.URL.Path == "/v1/reservations" && r.Method == http.MethodPost {
		h.reserve(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/reservations/") {
		rideID := strings.TrimPrefix(r.URL.Path, "/v1/reservations/")
		switch r.Method {
		case http.MethodGet:
			h.getReservation(w, r, rideID)
			return
		case http.MethodDelete:
			h.release(w, r, rideID)
			return
		}
	}
	httpapi.WriteError(w, r, 404, "not_found", "resource not found")
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	page, problem := httpapi.ParsePageQuery(r)
	if problem != nil {
		writeProblem(w, r, problem)
		return
	}
	asOf := h.clock().UTC()
	last := ""
	epoch, err := database.DatasetEpoch(r.Context(), h.db)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	if page.Cursor != "" {
		var err error
		asOf, last, err = httpapi.DecodeCursor(page.Cursor, "fleet:"+epoch, page.Limit)
		if err != nil {
			httpapi.WriteError(w, r, 400, "invalid_request", "cursor is invalid")
			return
		}
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT v.vehicle_id,v.zone,v.seats,v.availability,v.observed_at,v.maintenance,v.battery_percent, EXISTS(SELECT 1 FROM fleet_reservations x WHERE x.vehicle_id=v.vehicle_id AND x.state='reserved') FROM fleet_vehicles v WHERE v.vehicle_id>$1 ORDER BY v.vehicle_id LIMIT $2`, last, page.Limit+1)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	defer rows.Close()
	items := make([]Vehicle, 0, page.Limit+1)
	for rows.Next() {
		var v Vehicle
		var stored string
		var maintenance, active bool
		var battery int
		if err := rows.Scan(&v.VehicleID, &v.Zone, &v.Seats, &stored, &v.ObservedAt, &maintenance, &battery, &active); err != nil {
			httpapi.WriteError(w, r, 500, "internal_error", "could not read fleet")
			return
		}
		v.Availability = effectiveAvailability(stored, v.ObservedAt, maintenance, battery, active, asOf)
		v.ObservedAt = v.ObservedAt.UTC()
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		dbUnavailable(w, r)
		return
	}
	var next *string
	if len(items) > page.Limit {
		items = items[:page.Limit]
		c := httpapi.EncodeCursor("fleet:"+epoch, page.Limit, asOf, items[len(items)-1].VehicleID)
		next = &c
	}
	httpapi.WriteJSON(w, 200, FleetPage{Items: items, NextCursor: next, AsOf: asOf})
}
func (h *Handler) getVehicle(w http.ResponseWriter, r *http.Request, id string) {
	if !entityID.MatchString(id) {
		httpapi.WriteError(w, r, 400, "invalid_request", "vehicle ID is invalid")
		return
	}
	asOf := h.clock().UTC()
	var v Vehicle
	var stored string
	var maintenance, active bool
	var battery int
	err := h.db.QueryRowContext(r.Context(), `SELECT v.vehicle_id,v.zone,v.seats,v.availability,v.observed_at,v.maintenance,v.battery_percent, EXISTS(SELECT 1 FROM fleet_reservations x WHERE x.vehicle_id=v.vehicle_id AND x.state='reserved') FROM fleet_vehicles v WHERE v.vehicle_id=$1`, id).Scan(&v.VehicleID, &v.Zone, &v.Seats, &stored, &v.ObservedAt, &maintenance, &battery, &active)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 404, "not_found", "vehicle not found")
		return
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	v.Availability = effectiveAvailability(stored, v.ObservedAt, maintenance, battery, active, asOf)
	v.ObservedAt = v.ObservedAt.UTC()
	w.Header().Set("X-As-Of", asOf.Format(time.RFC3339Nano))
	httpapi.WriteJSON(w, 200, v)
}

func (h *Handler) reserve(w http.ResponseWriter, r *http.Request) {
	var req ReserveRequest
	if p := httpapi.DecodeStrict(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	if !entityID.MatchString(req.RideID) || !zoneSet[req.PickupZone] || req.Passengers < 1 || req.Passengers > 4 {
		httpapi.WriteError(w, r, 400, "invalid_request", "ride, pickup zone or passenger count is invalid")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, req.RideID); err != nil {
		dbUnavailable(w, r)
		return
	}
	var old Reservation
	var oldZone sql.NullString
	var oldPassengers sql.NullInt64
	err = tx.QueryRowContext(r.Context(), `SELECT ride_id,vehicle_id,state,reserved_at,updated_at,pickup_zone,passengers FROM fleet_reservations WHERE ride_id=$1 FOR UPDATE`, req.RideID).Scan(&old.RideID, &old.VehicleID, &old.State, &old.ReservedAt, &old.UpdatedAt, &oldZone, &oldPassengers)
	if err == nil {
		if old.State == "released" {
			httpapi.WriteError(w, r, 409, "reservation_released", "ride reservation is permanently released")
			return
		}
		if !oldZone.Valid || oldZone.String != req.PickupZone || !oldPassengers.Valid || int(oldPassengers.Int64) != req.Passengers {
			httpapi.WriteError(w, r, 409, "idempotency_conflict", "ride reservation parameters differ")
			return
		}
		_ = tx.Commit()
		normalizeReservation(&old)
		httpapi.WriteJSON(w, 200, old)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		dbUnavailable(w, r)
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT vehicle_id,observed_at,availability,maintenance,battery_percent FROM fleet_vehicles WHERE zone=$1 AND seats >= $2 ORDER BY vehicle_id FOR UPDATE`, req.PickupZone, req.Passengers)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	type candidate struct {
		id          string
		observed    time.Time
		stored      string
		maintenance bool
		battery     int
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.observed, &c.stored, &c.maintenance, &c.battery); err != nil {
			rows.Close()
			dbUnavailable(w, r)
			return
		}
		candidates = append(candidates, c)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		dbUnavailable(w, r)
		return
	}
	var selected string
	// The inventory locks above can block behind another reservation request.
	// Evaluate freshness only after all candidate rows are locked.
	eligibilityAt := h.clock().UTC()
	for _, c := range candidates {
		if effectiveAvailability(c.stored, c.observed, c.maintenance, c.battery, false, eligibilityAt) != "available" {
			continue
		}
		var active bool
		if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM fleet_reservations WHERE vehicle_id=$1 AND state='reserved')`, c.id).Scan(&active); err != nil {
			dbUnavailable(w, r)
			return
		}
		if !active {
			selected = c.id
			break
		}
	}
	if selected == "" {
		httpapi.WriteError(w, r, 409, "no_capacity", "no fresh eligible vehicle in pickup zone")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO fleet_reservations(ride_id,vehicle_id,state,reserved_at,updated_at,pickup_zone,passengers) VALUES($1,$2,'reserved',$3,$3,$4,$5)`, req.RideID, selected, eligibilityAt, req.PickupZone, req.Passengers); err != nil {
		dbUnavailable(w, r)
		return
	}
	if err := tx.Commit(); err != nil {
		dbUnavailable(w, r)
		return
	}
	reservedAt := eligibilityAt
	vehicle := selected
	httpapi.WriteJSON(w, 201, Reservation{RideID: req.RideID, VehicleID: &vehicle, State: "reserved", ReservedAt: &reservedAt, UpdatedAt: eligibilityAt})
}
func (h *Handler) getReservation(w http.ResponseWriter, r *http.Request, id string) {
	if !entityID.MatchString(id) {
		httpapi.WriteError(w, r, 400, "invalid_request", "ride ID is invalid")
		return
	}
	var out Reservation
	err := h.db.QueryRowContext(r.Context(), `SELECT ride_id,vehicle_id,state,reserved_at,updated_at FROM fleet_reservations WHERE ride_id=$1`, id).Scan(&out.RideID, &out.VehicleID, &out.State, &out.ReservedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 404, "not_found", "reservation not found")
		return
	}
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	normalizeReservation(&out)
	httpapi.WriteJSON(w, 200, out)
}
func (h *Handler) release(w http.ResponseWriter, r *http.Request, id string) {
	if !entityID.MatchString(id) {
		httpapi.WriteError(w, r, 400, "invalid_request", "ride ID is invalid")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		dbUnavailable(w, r)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id); err != nil {
		dbUnavailable(w, r)
		return
	}
	now := h.clock().UTC()
	// Match trip start/completion lock order: vehicle first, reservation second.
	var candidateVehicle sql.NullString
	if err = tx.QueryRowContext(r.Context(), `SELECT vehicle_id FROM fleet_reservations WHERE ride_id=$1`, id).Scan(&candidateVehicle); err != nil && !errors.Is(err, sql.ErrNoRows) {
		dbUnavailable(w, r)
		return
	}
	if candidateVehicle.Valid {
		var activeTrip, activeRide sql.NullString
		err = tx.QueryRowContext(r.Context(), `SELECT active_trip_id,active_ride_id FROM fleet_vehicles WHERE vehicle_id=$1 FOR UPDATE`, candidateVehicle.String).Scan(&activeTrip, &activeRide)
		if errors.Is(err, sql.ErrNoRows) {
			httpapi.WriteError(w, r, 409, "version_conflict", "reservation vehicle is missing")
			return
		}
		if err != nil {
			dbUnavailable(w, r)
			return
		}
		if activeTrip.Valid && activeRide.Valid && activeRide.String == id {
			httpapi.WriteError(w, r, 409, "trip_active", "reservation belongs to an active simulation trip")
			return
		}
	}
	var out Reservation
	err = tx.QueryRowContext(r.Context(), `SELECT ride_id,vehicle_id,state,reserved_at,updated_at FROM fleet_reservations WHERE ride_id=$1 FOR UPDATE`, id).Scan(&out.RideID, &out.VehicleID, &out.State, &out.ReservedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO fleet_reservations(ride_id,state,updated_at) VALUES($1,'released',$2)`, id, now); err != nil {
			dbUnavailable(w, r)
			return
		}
		out = Reservation{RideID: id, State: "released", UpdatedAt: now}
	} else if err != nil {
		dbUnavailable(w, r)
		return
	} else if out.State == "reserved" {
		if _, err = tx.ExecContext(r.Context(), `UPDATE fleet_reservations SET state='released',updated_at=GREATEST($2,updated_at) WHERE ride_id=$1`, id, now); err != nil {
			dbUnavailable(w, r)
			return
		}
		if now.Before(out.UpdatedAt) {
			now = out.UpdatedAt
		}
		out.State = "released"
		out.UpdatedAt = now
	}
	if err = tx.Commit(); err != nil {
		dbUnavailable(w, r)
		return
	}
	normalizeReservation(&out)
	httpapi.WriteJSON(w, 200, out)
}
func normalizeReservation(r *Reservation) {
	r.UpdatedAt = r.UpdatedAt.UTC()
	if r.ReservedAt != nil {
		t := r.ReservedAt.UTC()
		r.ReservedAt = &t
	}
	if r.VehicleID != nil && *r.VehicleID == "" {
		r.VehicleID = nil
	}
}
func dbUnavailable(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteError(w, r, 503, "temporarily_unavailable", "fleet database is unavailable")
}

func writeProblem(w http.ResponseWriter, r *http.Request, p *httpapi.Problem) {
	httpapi.WriteError(w, r, p.Status, p.Code, p.Message)
}
