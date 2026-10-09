package ride

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxFleetTripResponse = 4096

type TripEffects interface {
	Start(context.Context, FleetStartTripCommand) (TripEffectResult, error)
	Complete(context.Context, FleetCompleteTripCommand) (TripEffectResult, error)
	Receipt(context.Context, string) (TripEffectResult, error)
}

type HTTPTripEffects struct {
	base   string
	client *http.Client
}

func NewHTTPTripEffects(base string, transport ...http.RoundTripper) *HTTPTripEffects {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if len(transport) > 0 {
		client.Transport = transport[0]
	}
	return &HTTPTripEffects{base: strings.TrimRight(base, "/"), client: client}
}

func (c *HTTPTripEffects) Start(ctx context.Context, command FleetStartTripCommand) (TripEffectResult, error) {
	path := "/v2/fleet/vehicles/" + command.VehicleID + "/trips/start"
	return c.call(ctx, http.MethodPost, path, command)
}

func (c *HTTPTripEffects) Complete(ctx context.Context, command FleetCompleteTripCommand) (TripEffectResult, error) {
	path := "/v2/fleet/vehicles/" + command.VehicleID + "/trips/completion"
	return c.call(ctx, http.MethodPost, path, command)
}

func (c *HTTPTripEffects) Receipt(ctx context.Context, eventID string) (TripEffectResult, error) {
	return c.call(ctx, http.MethodGet, "/v2/fleet/events/"+eventID, nil)
}

func (c *HTTPTripEffects) call(ctx context.Context, method, path string, body any) (TripEffectResult, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return TripEffectResult{}, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return TripEffectResult{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return TripEffectResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFleetTripResponse+1))
	if err != nil {
		return TripEffectResult{}, err
	}
	if len(data) > maxFleetTripResponse {
		return TripEffectResult{}, fmt.Errorf("fleet response exceeded %d bytes", maxFleetTripResponse)
	}
	out := TripEffectResult{Status: resp.StatusCode}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if len(data) > 0 {
			if err := json.Unmarshal(data, &out.Receipt); err != nil {
				return TripEffectResult{}, err
			}
		}
		return out, nil
	}
	var failure struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(data, &failure)
	out.Code = failure.Code
	return out, nil
}
