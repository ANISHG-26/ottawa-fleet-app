package ride

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type tripRepoStub struct {
	stubRepository
	event   TripEvent
	trip    Trip
	prepare int
	commit  int
}

func (s *tripRepoStub) GetTrip(context.Context, string) (Trip, error) { return s.trip, nil }
func (s *tripRepoStub) PrepareTripStart(context.Context, string, TripCommand) (TripEvent, Trip, error) {
	s.prepare++
	return s.event, s.trip, nil
}
func (s *tripRepoStub) PrepareTripCompletion(context.Context, string, TripCommand) (TripEvent, Trip, error) {
	s.prepare++
	return s.event, s.trip, nil
}
func (s *tripRepoStub) CommitTripEffect(_ context.Context, _ string, receipt FleetEffectReceipt) (Trip, error) {
	s.commit++
	s.trip.TripState = "in_progress"
	s.trip.Version = 2
	s.trip.VehicleVersion = receipt.AcceptedVehicleVersion
	return s.trip, nil
}
func (s *tripRepoStub) RejectTripEffect(context.Context, string, string) error { return nil }

type tripEffectsStub struct {
	receiptResult TripEffectResult
	postResult    TripEffectResult
	order         []string
}

func (s *tripEffectsStub) Start(context.Context, FleetStartTripCommand) (TripEffectResult, error) {
	s.order = append(s.order, "start")
	return s.postResult, nil
}
func (s *tripEffectsStub) Complete(context.Context, FleetCompleteTripCommand) (TripEffectResult, error) {
	s.order = append(s.order, "complete")
	return s.postResult, nil
}
func (s *tripEffectsStub) Receipt(context.Context, string) (TripEffectResult, error) {
	s.order = append(s.order, "receipt")
	return s.receiptResult, nil
}

func validTripCommand() TripCommand {
	return TripCommand{
		EventID: "event-trip-start-001", TripID: "trip-test-001", VehicleID: "vehicle-001",
		ReservationID: "ride-test-001", RunID: "run-test-001", RouteID: tripRouteID,
		ExpectedTripVersion: 1, ExpectedVehicleVersion: 4,
	}
}

func TestTripStartPersistsIntentThenAppliesFleetEffect(t *testing.T) {
	command := validTripCommand()
	fleetCommand := fleetStartCommand(command)
	fingerprint, _, err := payloadFingerprint(fleetCommand)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 8, 0, time.UTC)
	receipt := FleetEffectReceipt{EventID: command.EventID, Effect: "trip_start", RunID: command.RunID,
		RideID: command.ReservationID, TripID: command.TripID, VehicleID: command.VehicleID,
		ReservationID: command.ReservationID, PayloadFingerprint: fingerprint, AcceptedVehicleVersion: 5,
		StoredAt: now, Result: "applied"}
	store := &tripRepoStub{event: TripEvent{EventID: command.EventID, RideID: command.ReservationID, Effect: "trip_start", State: "pending"},
		trip: Trip{RideID: command.ReservationID, AssignmentState: "completed", TripState: "not_started", Version: 1}}
	effects := &tripEffectsStub{receiptResult: TripEffectResult{Status: http.StatusNotFound}, postResult: TripEffectResult{Status: http.StatusOK, Receipt: receipt}}
	api := &API{Store: store, Trips: effects}
	body, _ := json.Marshal(command)
	req := httptest.NewRequest(http.MethodPost, "/v2/rides/ride-test-001/trip", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	api.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if store.prepare != 1 || store.commit != 1 || strings.Join(effects.order, ",") != "receipt,start" {
		t.Fatalf("prepare=%d commit=%d effects=%v", store.prepare, store.commit, effects.order)
	}
	if !strings.Contains(res.Body.String(), `"trip_state":"in_progress"`) || !strings.Contains(res.Body.String(), `"version":2`) {
		t.Fatalf("unexpected trip response: %s", res.Body.String())
	}
}

func TestTripReplayReconcilesReceiptWithoutRepeatingEffect(t *testing.T) {
	command := validTripCommand()
	fingerprint, _, _ := payloadFingerprint(fleetStartCommand(command))
	receipt := FleetEffectReceipt{EventID: command.EventID, Effect: "trip_start", RunID: command.RunID,
		RideID: command.ReservationID, TripID: command.TripID, VehicleID: command.VehicleID,
		ReservationID: command.ReservationID, PayloadFingerprint: fingerprint, AcceptedVehicleVersion: 5,
		StoredAt: time.Now().UTC(), Result: "replayed"}
	store := &tripRepoStub{event: TripEvent{EventID: command.EventID, RideID: command.ReservationID, Effect: "trip_start", State: "pending"}}
	effects := &tripEffectsStub{receiptResult: TripEffectResult{Status: http.StatusOK, Receipt: receipt}}
	api := &API{Store: store, Trips: effects}
	body, _ := json.Marshal(command)
	req := httptest.NewRequest(http.MethodPost, "/v2/rides/ride-test-001/trip", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	api.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK || store.commit != 1 || strings.Join(effects.order, ",") != "receipt" {
		t.Fatalf("status=%d commit=%d effects=%v body=%s", res.Code, store.commit, effects.order, res.Body.String())
	}
}

func TestTripHTTPRejectsUnknownFieldsAndWrongReservation(t *testing.T) {
	api := &API{Store: &tripRepoStub{}, Trips: &tripEffectsStub{}}
	for _, body := range []string{
		`{"event_id":"event-trip-start-001","trip_id":"trip-test-001","vehicle_id":"vehicle-001","reservation_id":"ride-test-001","run_id":"run-test-001","route_id":"lansdowne-centretown-v1","expected_trip_version":1,"expected_vehicle_version":4,"unexpected":true}`,
		`{"event_id":"event-trip-start-001","trip_id":"trip-test-001","vehicle_id":"vehicle-001","reservation_id":"other-ride-001","run_id":"run-test-001","route_id":"lansdowne-centretown-v1","expected_trip_version":1,"expected_vehicle_version":4}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v2/rides/ride-test-001/trip", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		api.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d", body, res.Code)
		}
	}
}

func TestHTTPTripEffectsRejectsRedirectAndCapsResponse(t *testing.T) {
	targetCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			targetCalls++
			return
		}
		if r.URL.Path == "/v2/fleet/events/event-trip-start-001" {
			w.Header().Set("Location", "/target")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", maxFleetTripResponse+1)))
	}))
	defer server.Close()
	effects := NewHTTPTripEffects(server.URL)
	if effects.client.Timeout != 5*time.Second {
		t.Fatalf("timeout=%s", effects.client.Timeout)
	}
	redirect, err := effects.Receipt(context.Background(), "event-trip-start-001")
	if err != nil || redirect.Status != http.StatusFound || targetCalls != 0 {
		t.Fatalf("redirect=%+v err=%v target calls=%d", redirect, err, targetCalls)
	}
	_, err = effects.Start(context.Background(), fleetStartCommand(validTripCommand()))
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("oversized response error=%v", err)
	}
}

func TestHTTPTripEffectsSendsOwnerCommandAndAcceptsMatchingReceipt(t *testing.T) {
	command := fleetStartCommand(validTripCommand())
	fingerprint, _, _ := payloadFingerprint(command)
	var request FleetStartTripCommand
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/fleet/vehicles/vehicle-001/trips/start" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode command: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FleetEffectReceipt{
			EventID: command.EventID, Effect: "trip_start", RunID: command.RunID,
			RideID: command.RideID, TripID: command.TripID, VehicleID: command.VehicleID,
			ReservationID: command.ReservationID, PayloadFingerprint: fingerprint,
			AcceptedVehicleVersion: command.ExpectedVehicleVersion + 1,
			StoredAt:               time.Now().UTC(), Result: "applied",
		})
	}))
	defer server.Close()
	effects := NewHTTPTripEffects(server.URL)
	result, err := effects.Start(context.Background(), command)
	if err != nil || result.Status != http.StatusOK || result.Receipt.PayloadFingerprint != fingerprint {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if request.ReservationID != command.RideID || request.RideID != command.RideID ||
		request.ExpectedVehicleVersion != command.ExpectedVehicleVersion {
		t.Fatalf("owner command mismatch: %+v", request)
	}
}
