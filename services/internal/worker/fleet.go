package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Reservation struct {
	RideID    string `json:"ride_id"`
	VehicleID string `json:"vehicle_id"`
	State     string `json:"state"`
}
type fleetError struct {
	Code string `json:"code"`
}
type FleetResult struct {
	Reservation Reservation
	Status      int
	Code        string
}

type Fleet interface {
	Get(context.Context, string, string) (FleetResult, error)
	Reserve(context.Context, Job, string) (FleetResult, error)
	Release(context.Context, string, string) (FleetResult, error)
}

type HTTPFleet struct {
	base   string
	client *http.Client
	logger *slog.Logger
}

func NewHTTPFleet(base string, transport ...http.RoundTripper) *HTTPFleet {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if len(transport) > 0 {
		client.Transport = transport[0]
	}
	return &HTTPFleet{base: strings.TrimRight(base, "/"), logger: slog.Default(), client: client}
}

func (c *HTTPFleet) Get(ctx context.Context, rideID, origin string) (FleetResult, error) {
	return c.call(ctx, http.MethodGet, "/v1/reservations/"+rideID, nil, origin)
}
func (c *HTTPFleet) Reserve(ctx context.Context, j Job, origin string) (FleetResult, error) {
	body := struct {
		RideID     string `json:"ride_id"`
		PickupZone string `json:"pickup_zone"`
		Passengers int    `json:"passengers"`
	}{j.RideID, j.Request.PickupZone, j.Request.Passengers}
	return c.call(ctx, http.MethodPost, "/v1/reservations", body, origin)
}
func (c *HTTPFleet) Release(ctx context.Context, rideID, origin string) (FleetResult, error) {
	return c.call(ctx, http.MethodDelete, "/v1/reservations/"+rideID, nil, origin)
}

func (c *HTTPFleet) call(ctx context.Context, method, path string, body any, origin string) (FleetResult, error) {
	var rd io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return FleetResult{}, e
		}
		rd = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if e != nil {
		return FleetResult{}, e
	}
	rid, e := newOutgoingID()
	if e != nil {
		return FleetResult{}, e
	}
	req.Header.Set("X-Request-ID", rid)
	req.Header.Set("X-Origin-Request-ID", origin)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	started := time.Now()
	resp, e := c.client.Do(req)
	if e != nil {
		c.logger.Warn("fleet request failed", "outgoing_request_id", rid, "origin_request_id", origin, "method", method, "path", path, "duration_ms", time.Since(started).Milliseconds(), "error", e)
		return FleetResult{}, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if e != nil {
		return FleetResult{}, e
	}
	if len(data) > 4096 {
		return FleetResult{}, fmt.Errorf("fleet response exceeded limit")
	}
	out := FleetResult{Status: resp.StatusCode}
	c.logger.Info("fleet request completed", "outgoing_request_id", rid, "origin_request_id", origin, "method", method, "path", path, "status", resp.StatusCode, "duration_ms", time.Since(started).Milliseconds())
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if len(data) > 0 {
			if e = json.Unmarshal(data, &out.Reservation); e != nil {
				return FleetResult{}, e
			}
		}
		return out, nil
	}
	var fe fleetError
	_ = json.Unmarshal(data, &fe)
	out.Code = fe.Code
	return out, nil
}

func newOutgoingID() (string, error) {
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return "req-" + hex.EncodeToString(b[:]), nil
}

func NewOwner() (string, error) {
	var b [8]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return "worker-" + hex.EncodeToString(b[:]), nil
}
