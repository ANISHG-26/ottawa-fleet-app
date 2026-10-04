package simulation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunWorker is restart-safe and only one process can dispatch at a time.
func (h *Handler) RunWorker(ctx context.Context) error {
	if !h.config.Enabled {
		<-ctx.Done()
		return nil
	}
	if h.db == nil {
		return errors.New("simulation database is unavailable")
	}
	conn, err := h.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for ctx.Err() == nil {
		var leader bool
		if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended('simulation-controller-dispatcher',0))`).Scan(&leader); err != nil {
			return err
		}
		if leader {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtextextended('simulation-controller-dispatcher',0))`)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := h.advance(ctx); err != nil { /* transient owner/database failures resume from the durable ledger */
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (h *Handler) advance(ctx context.Context) error {
	rows, err := h.db.QueryContext(ctx, `SELECT run_id,state,manifest,deadline_at,COALESCE(stop_requested_at,created_at),drain_deadline_at,COALESCE(terminal_reason,'') FROM simulation_runs WHERE state IN ('scheduled','running','stopping') ORDER BY created_at LIMIT 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	var runID, state, reason string
	var raw []byte
	var deadline, stopped, drain time.Time
	if err = rows.Scan(&runID, &state, &raw, &deadline, &stopped, &drain, &reason); err != nil {
		return err
	}
	var m Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	now := h.clock().UTC()
	if state != "stopping" && !now.Before(deadline) {
		_, err = h.db.ExecContext(ctx, `UPDATE simulation_runs SET state='stopping',terminal_reason='deadline',stop_requested_at=COALESCE(stop_requested_at,$2),drain_deadline_at=$3,version=version+1 WHERE run_id=$1 AND state IN ('scheduled','running')`, runID, now, now.Add(10*time.Second))
		if err != nil {
			return err
		}
		state = "stopping"
		reason = "deadline"
		drain = now.Add(10 * time.Second)
	}
	if state != "stopping" && state == "scheduled" {
		_, err = h.db.ExecContext(ctx, `UPDATE simulation_runs SET state='running',started_at=COALESCE(started_at,$2),version=version+1 WHERE run_id=$1 AND state='scheduled'`, runID, now)
		if err != nil {
			return err
		}
		state = "running"
	}
	requests, err := h.db.QueryContext(ctx, `SELECT sequence,ride_id,idempotency_key,trip_id,scheduled_at,assignment_state,trip_state,vehicle_id FROM simulation_requests WHERE run_id=$1 ORDER BY sequence`, runID)
	if err != nil {
		return err
	}
	type req struct {
		seq                            int
		ride, key, trip                string
		scheduled                      time.Time
		assignment, tripState, vehicle string
	}
	list := []req{}
	for requests.Next() {
		var x req
		var ride, vehicle sql.NullString
		if err = requests.Scan(&x.seq, &ride, &x.key, &x.trip, &x.scheduled, &x.assignment, &x.tripState, &vehicle); err != nil {
			requests.Close()
			return err
		}
		x.ride = ride.String
		x.vehicle = vehicle.String
		list = append(list, x)
	}
	err = requests.Err()
	requests.Close()
	if err != nil {
		return err
	}
	inflight := 0
	var dispatchErr error
	for _, x := range list {
		// A completed trip no longer occupies an execution slot even though its
		// Ride assignment remains assigned for the lifetime of the request.
		if x.assignment == "accepted" || (x.assignment == "assigned" && x.tripState != "completed" && x.tripState != "cancelled" && x.tripState != "unfinished") {
			inflight++
		}
	}
	for _, x := range list {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if x.assignment == "pending" {
			if state == "stopping" || now.Before(x.scheduled) || inflight >= m.MaxInFlight {
				continue
			}
			if now.After(x.scheduled.Add(time.Duration(m.DurationSeconds) * time.Second)) {
				_, _ = h.db.ExecContext(ctx, `UPDATE simulation_requests SET assignment_state='unfinished' WHERE run_id=$1 AND sequence=$2`, runID, x.seq)
				continue
			}
			if err = h.submitRide(ctx, runID, x); err != nil {
				continue
			}
			inflight++
			continue
		}
		if x.ride == "" || x.assignment == "failed" || x.assignment == "unfinished" || x.tripState == "completed" {
			continue
		}
		if x.assignment == "accepted" {
			var ride struct {
				State     string  `json:"state"`
				VehicleID *string `json:"vehicle_id"`
			}
			if err = h.callJSON(ctx, http.MethodGet, h.config.RideURL+"/v1/rides/"+x.ride, nil, "", &ride); err != nil {
				continue
			}
			if ride.State == "failed" {
				_, _ = h.db.ExecContext(ctx, `UPDATE simulation_requests SET assignment_state='failed',trip_state='cancelled' WHERE run_id=$1 AND sequence=$2`, runID, x.seq)
				_, _ = h.db.ExecContext(ctx, `UPDATE simulation_events SET result='failed' WHERE run_id=$1 AND request_sequence=$2 AND result='pending'`, runID, x.seq)
				continue
			}
			if ride.State != "completed" || ride.VehicleID == nil {
				continue
			}
			x.vehicle = *ride.VehicleID
			_, err = h.db.ExecContext(ctx, `UPDATE simulation_requests SET assignment_state='assigned',vehicle_id=$3 WHERE run_id=$1 AND sequence=$2`, runID, x.seq, x.vehicle)
			if err != nil {
				return err
			}
			x.assignment = "assigned"
		}
		if state == "stopping" && x.tripState == "not_started" {
			var pending bool
			if err = h.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM simulation_events WHERE run_id=$1 AND request_sequence=$2 AND kind='trip_start' AND payload IS NOT NULL)`, runID, x.seq).Scan(&pending); err != nil {
				return err
			}
			if !pending {
				continue
			}
		}
		if x.tripState == "not_started" {
			if now.Before(x.scheduled) {
				continue
			}
			if h.rateLimited(ctx, runID, m.EventRatePerSecond, now) {
				continue
			}
			if err = h.advanceTrip(ctx, runID, x, false); err != nil {
				dispatchErr = err
				continue
			}
		}
		if x.tripState == "in_progress" {
			if h.rateLimited(ctx, runID, m.EventRatePerSecond, now) {
				continue
			}
			if err = h.advanceTrip(ctx, runID, x, true); err != nil {
				dispatchErr = err
				continue
			}
		}
	}
	if err = h.finishRun(ctx, runID, state, reason, drain, now); err != nil {
		return err
	}
	return dispatchErr
}

func (h *Handler) submitRide(ctx context.Context, runID string, x struct {
	seq                            int
	ride, key, trip                string
	scheduled                      time.Time
	assignment, tripState, vehicle string
}) error {
	body := map[string]any{"pickup_zone": "lansdowne", "dropoff_zone": "centretown", "passengers": 1}
	var result struct {
		RideID string `json:"ride_id"`
		State  string `json:"state"`
	}
	if err := h.callJSON(ctx, http.MethodPost, h.config.RideURL+"/v1/rides", body, x.key, &result); err != nil {
		return err
	}
	if !idPattern.MatchString(result.RideID) {
		return errors.New("Ride returned invalid identity")
	}
	_, err := h.db.ExecContext(ctx, `UPDATE simulation_requests SET ride_id=$3,assignment_state='accepted',submitted_at=COALESCE(submitted_at,$4) WHERE run_id=$1 AND sequence=$2`, runID, x.seq, result.RideID, h.clock().UTC())
	return err
}
func (h *Handler) rateLimited(ctx context.Context, runID string, rate int, now time.Time) bool {
	var n int
	if h.db.QueryRowContext(ctx, `SELECT count(*) FROM simulation_events WHERE run_id=$1 AND issued_at>$2::timestamptz-interval '1 second'`, runID, now).Scan(&n) != nil {
		return true
	}
	return n >= rate
}

func (h *Handler) advanceTrip(ctx context.Context, runID string, x struct {
	seq                            int
	ride, key, trip                string
	scheduled                      time.Time
	assignment, tripState, vehicle string
}, complete bool) error {
	var trip struct {
		RideID          string     `json:"ride_id"`
		AssignmentState string     `json:"assignment_state"`
		TripState       string     `json:"trip_state"`
		TripID          string     `json:"trip_id"`
		VehicleID       string     `json:"vehicle_id"`
		StartedAt       *time.Time `json:"started_at"`
		Version         int64      `json:"version"`
	}
	if err := h.callJSON(ctx, http.MethodGet, h.config.RideURL+"/v2/rides/"+x.ride+"/trip", nil, "", &trip); err != nil {
		return err
	}
	if complete && trip.TripState == "completed" {
		if trip.TripID != x.trip || trip.VehicleID != x.vehicle {
			return errors.New("Ride completion identity differs from durable request")
		}
		_, err := h.db.ExecContext(ctx, `UPDATE simulation_requests SET trip_state='completed' WHERE run_id=$1 AND sequence=$2`, runID, x.seq)
		if err != nil {
			return err
		}
		return h.markEventApplied(ctx, runID, x.seq, "trip_complete")
	}
	if !complete && trip.TripState == "in_progress" {
		if trip.TripID != x.trip || trip.VehicleID != x.vehicle {
			return errors.New("Ride start identity differs from durable request")
		}
		_, err := h.db.ExecContext(ctx, `UPDATE simulation_requests SET trip_state='in_progress' WHERE run_id=$1 AND sequence=$2`, runID, x.seq)
		if err != nil {
			return err
		}
		return h.markEventApplied(ctx, runID, x.seq, "trip_start")
	}
	if complete {
		if trip.TripState != "in_progress" || trip.StartedAt == nil || h.clock().UTC().Before(trip.StartedAt.Add(8*time.Second)) {
			return errors.New("trip is not due for completion")
		}
	} else if trip.TripState != "not_started" {
		return errors.New("trip is not ready to start")
	}
	var pos struct {
		OperationalState string `json:"operational_state"`
		VehicleVersion   int64  `json:"vehicle_version"`
	}
	if err := h.callJSON(ctx, http.MethodGet, h.config.FleetURL+"/v2/fleet/vehicles/"+x.vehicle+"/position", nil, "", &pos); err != nil {
		return err
	}
	kind := "trip_start"
	if complete {
		kind = "trip_complete"
	}
	var eventID string
	var sequence int
	if err := h.db.QueryRowContext(ctx, `SELECT event_id,sequence FROM simulation_events WHERE run_id=$1 AND request_sequence=$2 AND kind=$3`, runID, x.seq, kind).Scan(&eventID, &sequence); err != nil {
		return err
	}
	var payload []byte
	err := h.db.QueryRowContext(ctx, `SELECT payload FROM simulation_events WHERE event_id=$1`, eventID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil {
		return err
	}
	if pos.OperationalState != "reserved" && len(payload) == 0 && !complete {
		return errors.New("vehicle reservation is not ready for a new start event")
	}
	if len(payload) == 0 {
		tripID := x.trip
		if trip.TripID != "" {
			tripID = trip.TripID
		}
		expectedTrip := trip.Version
		if complete && expectedTrip < 2 {
			expectedTrip = 2
		}
		command := map[string]any{"event_id": eventID, "trip_id": tripID, "vehicle_id": x.vehicle, "reservation_id": x.ride, "run_id": runID, "route_id": routeID, "expected_trip_version": expectedTrip, "expected_vehicle_version": pos.VehicleVersion}
		payload, err = json.Marshal(command)
		if err != nil {
			return err
		}
		due := h.clock().UTC()
		if complete && trip.StartedAt != nil {
			due = trip.StartedAt.Add(8 * time.Second)
		}
		_, err = h.db.ExecContext(ctx, `UPDATE simulation_events SET ride_id=$2,trip_id=$3,vehicle_id=$4,expected_trip_version=$5,expected_vehicle_version=$6,payload=$7::jsonb,scheduled_at=$8,issued_at=COALESCE(issued_at,$9) WHERE event_id=$1 AND payload IS NULL`, eventID, x.ride, tripID, x.vehicle, expectedTrip, pos.VehicleVersion, string(payload), due, h.clock().UTC())
		if err != nil {
			return err
		}
	} else {
		if trip.TripState == "in_progress" && !complete {
			return nil
		}
	}
	if complete && h.clock().UTC().Before(trip.StartedAt.Add(8*time.Second)) {
		return nil
	}
	path := "/trip"
	if complete {
		path = "/trip/completion"
	}
	if err = h.callJSON(ctx, http.MethodPost, h.config.RideURL+"/v2/rides/"+x.ride+path, json.RawMessage(payload), "", nil); err != nil {
		return err
	}
	// A 202/pending representation means Ride has durably accepted the event,
	// not that the Fleet effect is committed. Only its current trip view settles it.
	var confirmed struct {
		TripState string `json:"trip_state"`
		TripID    string `json:"trip_id"`
		VehicleID string `json:"vehicle_id"`
	}
	if err = h.callJSON(ctx, http.MethodGet, h.config.RideURL+"/v2/rides/"+x.ride+"/trip", nil, "", &confirmed); err != nil {
		return err
	}
	if confirmed.TripID != x.trip || confirmed.VehicleID != x.vehicle {
		return errors.New("Ride trip identity does not match durable event")
	}
	if complete && confirmed.TripState != "completed" {
		return errors.New("Ride completion remains pending")
	}
	if !complete && confirmed.TripState != "in_progress" && confirmed.TripState != "completed" {
		return errors.New("Ride start remains pending")
	}
	if complete {
		_, err = h.db.ExecContext(ctx, `UPDATE simulation_events SET result='applied' WHERE event_id=$1`, eventID)
		if err != nil {
			return err
		}
		_, err = h.db.ExecContext(ctx, `UPDATE simulation_requests SET trip_state='completed' WHERE run_id=$1 AND sequence=$2`, runID, x.seq)
		if err != nil {
			return err
		}
		return err
	}
	_, err = h.db.ExecContext(ctx, `UPDATE simulation_events SET result='applied' WHERE event_id=$1`, eventID)
	if err != nil {
		return err
	}
	_, err = h.db.ExecContext(ctx, `UPDATE simulation_requests SET trip_state='in_progress' WHERE run_id=$1 AND sequence=$2`, runID, x.seq)
	if err != nil {
		return err
	}
	if err != nil {
		return err
	}
	var completeEvent string
	if err = h.db.QueryRowContext(ctx, `SELECT event_id FROM simulation_events WHERE run_id=$1 AND request_sequence=$2 AND kind='trip_complete'`, runID, x.seq).Scan(&completeEvent); err != nil {
		return err
	}
	_, err = h.db.ExecContext(ctx, `UPDATE simulation_events SET scheduled_at=$2 WHERE event_id=$1`, completeEvent, h.clock().UTC().Add(8*time.Second))
	_ = sequence
	return err
}

func (h *Handler) markEventApplied(ctx context.Context, runID string, requestSequence int, kind string) error {
	result, err := h.db.ExecContext(ctx, `UPDATE simulation_events SET result='applied' WHERE run_id=$1 AND request_sequence=$2 AND kind=$3 AND result='pending'`, runID, requestSequence, kind)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
	}
	return err
}

func (h *Handler) callJSON(ctx context.Context, method, target string, body any, idempotency string, out any) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("owner URL is not configured")
	}
	var reader io.Reader
	if body != nil {
		var b []byte
		switch v := body.(type) {
		case json.RawMessage:
			b = v
		default:
			var err error
			b, err = json.Marshal(body)
			if err != nil {
				return err
			}
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 {
		return errors.New("owner response invalid or too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("owner returned HTTP %d", resp.StatusCode)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
func (h *Handler) finishRun(ctx context.Context, runID, state, reason string, drain, now time.Time) error {
	var total, done, requestsDone, active int
	if err := h.db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE result IN ('applied','replayed','rejected','failed')), (SELECT count(*) FROM simulation_requests WHERE run_id=$1 AND trip_state IN ('completed','cancelled')), (SELECT count(*) FROM simulation_requests WHERE run_id=$1 AND trip_state='in_progress') FROM simulation_events WHERE run_id=$1`, runID).Scan(&total, &done, &requestsDone, &active); err != nil {
		return err
	}
	var reqTotal int
	if err := h.db.QueryRowContext(ctx, `SELECT count(*) FROM simulation_requests WHERE run_id=$1`, runID).Scan(&reqTotal); err != nil {
		return err
	}
	if done == total && requestsDone == reqTotal {
		terminal := "completed"
		why := "all_events_complete"
		var failures int
		if err := h.db.QueryRowContext(ctx, `SELECT count(*) FROM simulation_events WHERE run_id=$1 AND result IN ('rejected','failed')`, runID).Scan(&failures); err != nil {
			return err
		}
		if failures > 0 {
			terminal = "failed"
			why = "execution_failure"
		}
		if state == "stopping" && reason != "deadline" {
			terminal = "stopped"
			why = reason
			if why == "" {
				why = "requested_stop"
			}
		}
		_, err := h.db.ExecContext(ctx, `UPDATE simulation_runs SET state=$2,terminal_reason=$3,completed_at=$4,version=version+1 WHERE run_id=$1 AND state IN ('running','scheduled','stopping')`, runID, terminal, why, now)
		return err
	}
	if state == "stopping" && !now.Before(drain) {
		var unfinishedTrips, unfinishedReq, unfinishedEvents int
		if err := h.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM simulation_requests WHERE run_id=$1 AND trip_state='in_progress'),(SELECT count(*) FROM simulation_requests WHERE run_id=$1 AND trip_state<>'completed'),(SELECT count(*) FROM simulation_events WHERE run_id=$1 AND result='pending')`, runID).Scan(&unfinishedTrips, &unfinishedReq, &unfinishedEvents); err != nil {
			return err
		}
		_, err := h.db.ExecContext(ctx, `UPDATE simulation_requests SET trip_state=CASE WHEN trip_state='in_progress' THEN 'unfinished' ELSE trip_state END,assignment_state=CASE WHEN assignment_state IN ('pending','accepted','assigned') THEN 'unfinished' ELSE assignment_state END WHERE run_id=$1 AND trip_state<>'completed'`, runID)
		if err != nil {
			return err
		}
		_, err = h.db.ExecContext(ctx, `UPDATE simulation_runs SET state='stopped',terminal_reason=$2,completed_at=$3,incomplete_trips=$4,incomplete_requests=$5,incomplete_events=$6,version=version+1 WHERE run_id=$1 AND state='stopping'`, runID, reason, now, unfinishedTrips, unfinishedReq, unfinishedEvents)
		return err
	}
	_ = active
	return nil
}
