package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Seed              int64
	Count             int
	Interval          time.Duration
	Observe           time.Duration
	Mode              string
	FleetURL          string
	RideURL           string
	FaultLatencyMS    int
	FaultErrorPercent int
}
type Outcome struct {
	Index     int    `json:"index"`
	Key       string `json:"idempotency_key"`
	Status    int    `json:"status"`
	RideID    string `json:"ride_id,omitempty"`
	State     string `json:"state,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}
type Report struct {
	Seed       int64     `json:"seed"`
	Mode       string    `json:"mode"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Accepted   []Outcome `json:"accepted"`
	Rejected   []Outcome `json:"rejected"`
	Unfinished []Outcome `json:"unfinished"`
}
type ridePayload struct {
	PickupZone  string `json:"pickup_zone"`
	DropoffZone string `json:"dropoff_zone"`
	Passengers  int    `json:"passengers"`
}
type rideReply struct {
	RideID      string `json:"ride_id"`
	State       string `json:"state"`
	FailureCode string `json:"failure_code"`
}
type errorReply struct {
	Code string `json:"code"`
}

func (c Config) Validate() error {
	if c.Seed < 0 {
		return errors.New("seed must be non-negative")
	}
	if c.Count < 1 || c.Count > 100 {
		return errors.New("count must be from 1 through 100")
	}
	if c.Interval < 100*time.Millisecond || c.Interval > 10*time.Second {
		return errors.New("interval must be from 100ms through 10s")
	}
	if time.Duration(c.Count)*c.Interval > 60*time.Second {
		return errors.New("submission duration exceeds 60 seconds")
	}
	if c.Observe < 0 || c.Observe > 60*time.Second {
		return errors.New("observation window must be at most 60 seconds")
	}
	if c.Mode != "surge" && c.Mode != "normal" && c.Mode != "fault" {
		return errors.New("mode must be surge, normal or fault")
	}
	if c.FaultLatencyMS < 0 || c.FaultLatencyMS > 1000 || c.FaultErrorPercent < 0 || c.FaultErrorPercent > 50 {
		return errors.New("fault values exceed supported bounds")
	}
	if c.Mode != "fault" && (c.FaultLatencyMS != 0 || c.FaultErrorPercent != 0) {
		return errors.New("fault controls require --mode fault")
	}
	if c.Mode == "fault" && c.FaultLatencyMS == 0 && c.FaultErrorPercent == 0 {
		return errors.New("fault mode requires latency or error injection")
	}
	if err := localURL(c.RideURL, 8081); err != nil {
		return fmt.Errorf("ride URL: %w", err)
	}
	if err := localURL(c.FleetURL, 8080); err != nil {
		return fmt.Errorf("fleet URL: %w", err)
	}
	return nil
}

func Inputs(seed int64, count int, mode string) []Outcome {
	out := make([]Outcome, count)
	for i := range out {
		key := fmt.Sprintf("%s_%010d_%04d", mode, seed, i+1)
		if mode == "surge" && seed == 1 {
			key = fmt.Sprintf("surge_%04d", i+1)
		}
		out[i] = Outcome{Index: i + 1, Key: key}
	}
	return out
}

func Run(ctx context.Context, c Config) (report Report, runErr error) {
	if err := c.Validate(); err != nil {
		return Report{}, err
	}
	report = Report{Seed: c.Seed, Mode: c.Mode, StartedAt: time.Now().UTC(), Accepted: []Outcome{}, Rejected: []Outcome{}, Unfinished: []Outcome{}}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	faultApplied := false
	if c.Mode == "fault" {
		if err := setFault(ctx, client, c); err != nil {
			return report, err
		}
		faultApplied = true
	}
	defer func() {
		if faultApplied {
			rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := resetFault(rctx, client, c.FleetURL); err != nil && runErr == nil {
				runErr = fmt.Errorf("scenario ended but Fleet faults could not be reset: %w", err)
			}
		}
	}()
	inputs := Inputs(c.Seed, c.Count, c.Mode)
	var pending []Outcome
	submitCtx, submitCancel := context.WithTimeout(ctx, 60*time.Second)
	defer submitCancel()
	for idx, out := range inputs {
		if idx > 0 {
			timer := time.NewTimer(c.Interval)
			select {
			case <-submitCtx.Done():
				timer.Stop()
				if ctx.Err() != nil {
					report.EndedAt = time.Now().UTC()
					return report, ctx.Err()
				}
				for _, skipped := range inputs[idx:] {
					skipped.Status = 0
					skipped.ErrorCode = "submission_deadline"
					report.Rejected = append(report.Rejected, skipped)
				}
				break
			case <-timer.C:
			}
			if submitCtx.Err() != nil {
				break
			}
		}
		payload := ridePayload{PickupZone: "lansdowne", DropoffZone: "centretown", Passengers: 2}
		if c.Mode == "normal" {
			zones := []string{"centretown", "glebe", "lansdowne", "byward-market"}
			from := zones[(idx+int(c.Seed%4))%len(zones)]
			to := zones[(idx+1+int(c.Seed%4))%len(zones)]
			payload.PickupZone = from
			payload.DropoffZone = to
		}
		result := submit(submitCtx, client, c.RideURL, out.Key, payload)
		if result.RideID != "" {
			report.Accepted = append(report.Accepted, result)
			pending = append(pending, result)
		} else {
			report.Rejected = append(report.Rejected, result)
		}
	}
	if submitCtx.Err() != nil && ctx.Err() == nil {
		goto observe
	}
observe:
	deadline := time.Now().Add(c.Observe)
	observeCtx, observeCancel := context.WithDeadline(ctx, deadline)
	defer observeCancel()
	for len(pending) > 0 && time.Now().Before(deadline) {
		next := pending[:0]
		for _, item := range pending {
			state, code, err := getRide(observeCtx, client, c.RideURL, item.RideID)
			if err != nil {
				next = append(next, item)
				continue
			}
			for i := range report.Accepted {
				if report.Accepted[i].RideID == item.RideID {
					report.Accepted[i].State = state
					report.Accepted[i].ErrorCode = code
				}
			}
			if state != "completed" && state != "failed" {
				next = append(next, item)
			}
		}
		pending = next
		if len(pending) > 0 {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-observeCtx.Done():
				timer.Stop()
				if ctx.Err() != nil {
					report.EndedAt = time.Now().UTC()
					return report, ctx.Err()
				}
				break
			case <-timer.C:
			}
		}
	}
	for _, item := range pending {
		report.Unfinished = append(report.Unfinished, item)
	}
	report.EndedAt = time.Now().UTC()
	return report, nil
}

func submit(ctx context.Context, client *http.Client, base, key string, payload ridePayload) Outcome {
	out := Outcome{Key: key}
	body, _ := json.Marshal(payload)
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/rides", bytes.NewReader(body))
		if err != nil {
			out.ErrorCode = "request_error"
			return out
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		resp, err := client.Do(req)
		if err != nil {
			out.ErrorCode = "temporarily_unavailable"
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4097))
		resp.Body.Close()
		out.Status = resp.StatusCode
		if resp.StatusCode == 202 {
			var ride rideReply
			if json.Unmarshal(data, &ride) == nil {
				out.RideID = ride.RideID
				out.State = ride.State
				return out
			}
		}
		var er errorReply
		_ = json.Unmarshal(data, &er)
		out.ErrorCode = er.Code
		if resp.StatusCode < 500 {
			return out
		}
	}
	return out
}

func getRide(ctx context.Context, client *http.Client, base, id string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/rides/"+id, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("ride status returned %d", resp.StatusCode)
	}
	var result rideReply
	if err = json.Unmarshal(data, &result); err != nil {
		return "", "", err
	}
	return result.State, result.FailureCode, nil
}

func setFault(ctx context.Context, client *http.Client, c Config) error {
	body, _ := json.Marshal(map[string]int{"latency_ms": c.FaultLatencyMS, "error_percent": c.FaultErrorPercent})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.FleetURL, "/")+"/debug/faults", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fleet fault control rejected with status %d; start Fleet with FLEET_FAULT_CONTROL=1", resp.StatusCode)
	}
	return nil
}
func resetFault(ctx context.Context, client *http.Client, fleet string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(fleet, "/")+"/debug/faults/reset", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fleet fault reset returned %d", resp.StatusCode)
	}
	return nil
}

func localURL(raw string, port int) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		return errors.New("URL must specify an explicit loopback host and port")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("only localhost or a loopback IP is allowed")
		}
	}
	p, err := strconv.Atoi(portText)
	if err != nil || p != port {
		return fmt.Errorf("URL must use port %d", port)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && !ip.IsLoopback() {
		return errors.New("only loopback addresses are allowed")
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("URL must be plain HTTP without credentials, query or fragment")
	}
	return nil
}
