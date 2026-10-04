package simulation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
)

const routeID = "lansdowne-centretown-v1"

type Manifest struct {
	ScenarioID           string `json:"scenario_id"`
	FleetProfile         string `json:"fleet_profile"`
	IdempotencyKey       string `json:"idempotency_key"`
	EventRatePerSecond   int    `json:"event_rate_per_second"`
	RequestCount         int    `json:"request_count"`
	ExecutionEventCount  int    `json:"execution_event_count"`
	DurationSeconds      int    `json:"duration_seconds"`
	MaxInFlight          int    `json:"max_in_flight"`
	RouteDurationSeconds int    `json:"route_duration_seconds"`
	RouteID              string `json:"route_id"`
	StartZone            string `json:"start_zone"`
	EndZone              string `json:"end_zone"`
	Seed                 int    `json:"seed"`
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

func (m Manifest) Validate() error {
	if m.ScenarioID != "route20synthetic" || (m.FleetProfile != "route20synthetic" && m.FleetProfile != "default-six") || !keyPattern.MatchString(m.IdempotencyKey) {
		return errors.New("scenario, profile or idempotency_key is invalid")
	}
	if m.EventRatePerSecond < 1 || m.EventRatePerSecond > 4 || m.RequestCount < 1 || m.RequestCount > 20 || m.ExecutionEventCount < m.RequestCount*2 || m.ExecutionEventCount > 60 || m.DurationSeconds < 1 || m.DurationSeconds > 60 || m.MaxInFlight < 1 || m.MaxInFlight > 4 {
		return errors.New("manifest exceeds request, event, duration or concurrency bounds")
	}
	if m.RouteDurationSeconds != 8 || m.RouteID != routeID || m.StartZone != "lansdowne" || m.EndZone != "centretown" || m.Seed < 0 || m.Seed > 2147483647 {
		return errors.New("manifest route or seed is unsupported")
	}
	return nil
}

type Config struct {
	Enabled           bool
	FleetURL, RideURL string
	Clock             func() time.Time
	Client            *http.Client
}
type Handler struct {
	db            *sql.DB
	config        Config
	clock         func() time.Time
	client        *http.Client
	mux           http.Handler
	dispatchTurn  atomic.Uint32
	terminalMu    sync.Mutex
	terminalAfter string
}

func NewHandler(db *sql.DB, config Config) *Handler {
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	c := config.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	h := &Handler{db: db, config: config, clock: clock, client: c}
	h.mux = httpapi.RequestID(http.HandlerFunc(h.route))
	return h
}
func (h *Handler) Handler() http.Handler { return h.mux }
func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		httpapi.WriteJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/readyz" {
		h.ready(w, r)
		return
	}
	if r.URL.Path == "/metrics" {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, "simulation_controller_up 1\n")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v2/simulation/routes/") {
		h.getRoute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v2/simulation/runs") {
		h.runs(w, r)
		return
	}
	httpapi.WriteError(w, r, 404, "not_found", "resource not found")
}
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if h.db == nil || h.db.PingContext(ctx) != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller database unavailable")
		return
	}
	var compatible bool
	if err := h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_schema_migrations WHERE name='simulation/001_runs.sql')`).Scan(&compatible); err != nil || !compatible {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller migration unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]string{"status": "ready"})
}
func (h *Handler) getRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpapi.WriteError(w, r, 404, "not_found", "resource not found")
		return
	}
	if strings.TrimPrefix(r.URL.Path, "/v2/simulation/routes/") != routeID {
		httpapi.WriteError(w, r, 404, "not_found", "route not found")
		return
	}
	points := make([]point, 21)
	for i := range points {
		points[i] = routePoint(i)
	}
	httpapi.WriteJSON(w, 200, route{RouteID: routeID, RouteVersion: 1, StartZone: "lansdowne", EndZone: "centretown", DurationSeconds: 8, Points: points})
}
func (h *Handler) runs(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v2/simulation/runs")
	if path == "" || path == "/" {
		if r.Method == http.MethodPost {
			h.createRun(w, r)
			return
		}
		if r.Method == http.MethodGet {
			h.listRuns(w, r)
			return
		}
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || !idPattern.MatchString(parts[0]) {
		httpapi.WriteError(w, r, 400, "invalid_request", "run ID is invalid")
		return
	}
	if len(parts) == 1 {
		if r.Method == http.MethodGet {
			h.getRun(w, r, parts[0])
			return
		}
		if r.Method == http.MethodDelete {
			h.stopRun(w, r, parts[0])
			return
		}
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		h.listEvents(w, r, parts[0])
		return
	}
	httpapi.WriteError(w, r, 404, "not_found", "resource not found")
}
func (h *Handler) createRun(w http.ResponseWriter, r *http.Request) {
	if !h.config.Enabled {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "simulation writes are disabled")
		return
	}
	if h.db == nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller database unavailable")
		return
	}
	var request struct {
		Manifest Manifest `json:"manifest"`
	}
	if p := httpapi.DecodeStrict(w, r, &request); p != nil {
		httpapi.WriteError(w, r, p.Status, p.Code, p.Message)
		return
	}
	if err := request.Manifest.Validate(); err != nil {
		httpapi.WriteError(w, r, 400, "invalid_request", err.Error())
		return
	}
	if request.Manifest.FleetProfile != "route20synthetic" {
		httpapi.WriteError(w, r, 503, "profile_not_ready", "route20synthetic fleet profile is required")
		return
	}
	if err := h.checkFleetProfile(r.Context()); err != nil {
		httpapi.WriteError(w, r, 503, "profile_not_ready", "route20synthetic fleet profile is not ready")
		return
	}
	manifestJSON, _ := json.Marshal(request.Manifest)
	sum := sha256.Sum256(manifestJSON)
	fingerprint := hex.EncodeToString(sum[:])
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "simulation-idem:"+request.Manifest.IdempotencyKey); err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	old, err := scanRun(tx.QueryRowContext(r.Context(), `SELECT `+runColumns+` FROM simulation_runs WHERE idempotency_key=$1`, request.Manifest.IdempotencyKey))
	if err == nil {
		if old.ManifestFingerprint != fingerprint {
			httpapi.WriteError(w, r, 409, "idempotency_conflict", "idempotency key has different normalized manifest")
			return
		}
		if tx.Commit() != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
			return
		}
		httpapi.WriteJSON(w, 200, old)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	runID, err := newStableID("run-")
	if err != nil {
		httpapi.WriteError(w, r, 500, "temporarily_unavailable", "could not create run ID")
		return
	}
	now := h.clock().UTC()
	deadline := now.Add(time.Duration(request.Manifest.DurationSeconds) * time.Second)
	drain := deadline.Add(10 * time.Second)
	_, err = tx.ExecContext(r.Context(), `INSERT INTO simulation_runs(run_id,idempotency_key,manifest_fingerprint,manifest,state,created_at,deadline_at,drain_deadline_at) VALUES($1,$2,$3,$4::jsonb,'scheduled',$5,$6,$7)`, runID, request.Manifest.IdempotencyKey, fingerprint, string(manifestJSON), now, deadline, drain)
	if err != nil {
		httpapi.WriteError(w, r, 409, "run_active", "another simulation run is active")
		return
	}
	for i := 1; i <= request.Manifest.RequestCount; i++ {
		rideKey := fmt.Sprintf("%s-request-%02d", runID, i)
		tripID := fmt.Sprintf("trip-%s-%02d", strings.TrimPrefix(runID, "run-"), i)
		scheduled := now.Add(time.Duration(i-1) * time.Second / time.Duration(request.Manifest.EventRatePerSecond))
		_, err = tx.ExecContext(r.Context(), `INSERT INTO simulation_requests(run_id,sequence,idempotency_key,trip_id,scheduled_at) VALUES($1,$2,$3,$4,$5)`, runID, i, rideKey, tripID, scheduled)
		if err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "could not store requests")
			return
		}
		for j, kind := range []string{"trip_start", "trip_complete"} {
			seq := (i-1)*2 + j + 1
			eventID := fmt.Sprintf("evt-%s-%03d", strings.TrimPrefix(runID, "run-"), seq)
			due := scheduled
			if j == 1 {
				due = now.Add(time.Duration(request.Manifest.DurationSeconds)*time.Second + 10*time.Second)
			}
			_, err = tx.ExecContext(r.Context(), `INSERT INTO simulation_events(event_id,run_id,sequence,request_sequence,kind,trip_id,scheduled_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, eventID, runID, seq, i, kind, tripID, due)
			if err != nil {
				httpapi.WriteError(w, r, 503, "temporarily_unavailable", "could not store events")
				return
			}
		}
	}
	if err = tx.Commit(); err != nil {
		httpapi.WriteError(w, r, 409, "run_active", "another simulation run is active")
		return
	}
	got, err := loadRun(r.Context(), h.db, runID)
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, got)
}
func (h *Handler) checkFleetProfile(ctx context.Context) error {
	base, err := url.Parse(h.config.FleetURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return errors.New("Fleet URL is not configured")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(h.config.FleetURL, "/")+"/v1/fleet?limit=100", nil)
	if err != nil {
		return err
	}
	resp, err := h.client.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Fleet status %d", resp.StatusCode)
	}
	var page struct {
		Items []struct {
			VehicleID string `json:"vehicle_id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("Fleet response too large")
	}
	if err = json.Unmarshal(data, &page); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, v := range page.Items {
		ids[v.VehicleID] = true
	}
	if len(ids) != 20 || page.NextCursor != nil {
		return errors.New("Fleet inventory is not exactly 20 vehicles")
	}
	for i := 1; i <= 20; i++ {
		if !ids[fmt.Sprintf("vehicle-%03d", i)] {
			return errors.New("Fleet inventory IDs mismatch")
		}
	}
	return nil
}

func parseLimit(r *http.Request) (int, string, error) {
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 100 {
			return 0, "", errors.New("limit must be 1..100")
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), nil
}
func makeCursor(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
func readCursor(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	b, e := base64.RawURLEncoding.DecodeString(v)
	if e != nil || len(b) > 128 {
		return "", errors.New("cursor invalid")
	}
	return string(b), nil
}
func newStableID(prefix string) (string, error) {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

type point struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
type route struct {
	RouteID         string  `json:"route_id"`
	RouteVersion    int     `json:"route_version"`
	StartZone       string  `json:"start_zone"`
	EndZone         string  `json:"end_zone"`
	DurationSeconds int     `json:"duration_seconds"`
	Points          []point `json:"points"`
}

func routePoint(i int) point {
	if i < 0 {
		i = 0
	}
	if i > 20 {
		i = 20
	}
	return point{Latitude: math.Round((45.399+0.021*float64(i)/20)*1e6) / 1e6, Longitude: math.Round((-75.684-0.009*float64(i)/20)*1e6) / 1e6}
}

type Run struct {
	RunID               string     `json:"run_id"`
	State               string     `json:"state"`
	Manifest            Manifest   `json:"manifest"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	DeadlineAt          time.Time  `json:"deadline_at"`
	StopRequestedAt     *time.Time `json:"stop_requested_at,omitempty"`
	DrainDeadlineAt     time.Time  `json:"drain_deadline_at"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	TerminalReason      string     `json:"terminal_reason,omitempty"`
	IssuedEvents        int        `json:"issued_events"`
	CompletedEvents     int        `json:"completed_events"`
	IncompleteTrips     int        `json:"incomplete_trips"`
	IncompleteRequests  int        `json:"incomplete_requests"`
	IncompleteEvents    int        `json:"incomplete_events"`
	Version             int64      `json:"version"`
	ManifestFingerprint string     `json:"-"`
}

const runColumns = `run_id,state,manifest,created_at,started_at,deadline_at,stop_requested_at,drain_deadline_at,completed_at,COALESCE(terminal_reason,''),issued_events,completed_events,incomplete_trips,incomplete_requests,incomplete_events,version,manifest_fingerprint`

type scanner interface{ Scan(...any) error }

func scanRun(s scanner) (Run, error) {
	var out Run
	var body []byte
	err := s.Scan(&out.RunID, &out.State, &body, &out.CreatedAt, &out.StartedAt, &out.DeadlineAt, &out.StopRequestedAt, &out.DrainDeadlineAt, &out.CompletedAt, &out.TerminalReason, &out.IssuedEvents, &out.CompletedEvents, &out.IncompleteTrips, &out.IncompleteRequests, &out.IncompleteEvents, &out.Version, &out.ManifestFingerprint)
	if err == nil {
		err = json.Unmarshal(body, &out.Manifest)
		out.CreatedAt = out.CreatedAt.UTC()
		out.DeadlineAt = out.DeadlineAt.UTC()
		out.DrainDeadlineAt = out.DrainDeadlineAt.UTC()
		if out.StartedAt != nil {
			t := out.StartedAt.UTC()
			out.StartedAt = &t
		}
		if out.StopRequestedAt != nil {
			t := out.StopRequestedAt.UTC()
			out.StopRequestedAt = &t
		}
		if out.CompletedAt != nil {
			t := out.CompletedAt.UTC()
			out.CompletedAt = &t
		}
	}
	return out, err
}
func loadRun(ctx context.Context, db *sql.DB, id string) (Run, error) {
	return scanRun(db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM simulation_runs WHERE run_id=$1`, id))
}
func (h *Handler) getRun(w http.ResponseWriter, r *http.Request, id string) {
	v, e := loadRun(r.Context(), h.db, id)
	if errors.Is(e, sql.ErrNoRows) {
		httpapi.WriteError(w, r, 404, "not_found", "run not found")
		return
	}
	if e != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, v)
}
func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, cursor, e := parseLimit(r)
	if e != nil {
		httpapi.WriteError(w, r, 400, "invalid_request", e.Error())
		return
	}
	after, e := readCursor(cursor)
	if e != nil {
		httpapi.WriteError(w, r, 400, "invalid_request", "cursor invalid")
		return
	}
	if after != "" {
		if !strings.HasPrefix(after, "runs:") {
			httpapi.WriteError(w, r, 400, "invalid_request", "cursor belongs to a different endpoint")
			return
		}
		after = strings.TrimPrefix(after, "runs:")
	}
	rows, e := h.db.QueryContext(r.Context(), `SELECT `+runColumns+` FROM simulation_runs WHERE run_id>$1 ORDER BY run_id LIMIT $2`, after, limit+1)
	if e != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	defer rows.Close()
	items := []Run{}
	for rows.Next() {
		v, x := scanRun(rows)
		if x != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
			return
		}
		items = append(items, v)
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		c := makeCursor("runs:" + items[len(items)-1].RunID)
		next = &c
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request, runID string) {
	limit, cursor, e := parseLimit(r)
	if e != nil {
		httpapi.WriteError(w, r, 400, "invalid_request", e.Error())
		return
	}
	after, e := readCursor(cursor)
	if e != nil {
		httpapi.WriteError(w, r, 400, "invalid_request", "cursor invalid")
		return
	}
	n := 0
	if after != "" {
		prefix := "events:" + runID + ":"
		if !strings.HasPrefix(after, prefix) {
			httpapi.WriteError(w, r, 400, "invalid_request", "cursor belongs to a different run or endpoint")
			return
		}
		n, e = strconv.Atoi(strings.TrimPrefix(after, prefix))
		if e != nil || n < 0 {
			httpapi.WriteError(w, r, 400, "invalid_request", "cursor invalid")
			return
		}
	}
	rows, e := h.db.QueryContext(r.Context(), `SELECT event_id,run_id,sequence,kind,COALESCE(ride_id,''),trip_id,COALESCE(vehicle_id,''),expected_trip_version,expected_vehicle_version,scheduled_at,issued_at,result FROM simulation_events WHERE run_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, runID, n, limit+1)
	if e != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	defer rows.Close()
	items := []Event{}
	for rows.Next() {
		var v Event
		if e = rows.Scan(&v.EventID, &v.RunID, &v.Sequence, &v.Kind, &v.RideID, &v.TripID, &v.VehicleID, &v.ExpectedTripVersion, &v.ExpectedVehicleVersion, &v.ScheduledAt, &v.IssuedAt, &v.Result); e != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
			return
		}
		v.ScheduledAt = v.ScheduledAt.UTC()
		if v.IssuedAt != nil {
			t := v.IssuedAt.UTC()
			v.IssuedAt = &t
		}
		items = append(items, v)
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		c := makeCursor("events:" + runID + ":" + strconv.Itoa(items[len(items)-1].Sequence))
		next = &c
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

type Event struct {
	EventID                string     `json:"event_id"`
	RunID                  string     `json:"run_id"`
	Sequence               int        `json:"sequence"`
	Kind                   string     `json:"kind"`
	RideID                 string     `json:"ride_id,omitempty"`
	TripID                 string     `json:"trip_id,omitempty"`
	VehicleID              string     `json:"vehicle_id,omitempty"`
	ExpectedTripVersion    *int64     `json:"expected_trip_version,omitempty"`
	ExpectedVehicleVersion *int64     `json:"expected_vehicle_version,omitempty"`
	ScheduledAt            time.Time  `json:"scheduled_at"`
	IssuedAt               *time.Time `json:"issued_at,omitempty"`
	Result                 string     `json:"result"`
}

func (h *Handler) stopRun(w http.ResponseWriter, r *http.Request, id string) {
	if !h.config.Enabled {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "simulation writes are disabled")
		return
	}
	var request struct {
		Drain bool `json:"drain"`
	}
	if p := httpapi.DecodeStrict(w, r, &request); p != nil {
		httpapi.WriteError(w, r, p.Status, p.Code, p.Message)
		return
	}
	if !request.Drain {
		httpapi.WriteError(w, r, 400, "invalid_request", "drain must be true")
		return
	}
	now := h.clock().UTC()
	_, e := h.db.ExecContext(r.Context(), `UPDATE simulation_runs SET state='stopping',terminal_reason=COALESCE(NULLIF(terminal_reason,''),'requested_stop'),stop_requested_at=COALESCE(stop_requested_at,$2),drain_deadline_at=CASE WHEN state='stopping' THEN drain_deadline_at ELSE $2+interval '10 seconds' END,version=version+1 WHERE run_id=$1 AND state IN ('scheduled','running','stopping')`, id, now)
	if e != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "controller store unavailable")
		return
	}
	h.getRun(w, r, id)
}
