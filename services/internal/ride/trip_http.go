package ride

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
)

func (a *API) trip(w http.ResponseWriter, r *http.Request) {
	const prefix = "/v2/rides/"
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(parts) < 2 || !tripIDPattern.MatchString(parts[0]) || parts[1] != "trip" || len(parts) > 3 || (len(parts) == 3 && parts[2] != "completion") {
		httpapi.WriteError(w, r, 400, "invalid_request", "trip path is invalid")
		return
	}
	rideID := parts[0]
	complete := len(parts) == 3
	if r.Method == http.MethodGet && !complete {
		repo, ok := a.Store.(tripRepository)
		if !ok {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip store unavailable")
			return
		}
		trip, err := repo.GetTrip(r.Context(), rideID)
		if err != nil {
			writeTripError(w, r, err)
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, trip)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		httpapi.WriteError(w, r, 405, "invalid_request", "method not allowed")
		return
	}
	var command TripCommand
	if p := httpapi.DecodeStrict(w, r, &command); p != nil {
		httpapi.WriteError(w, r, p.Status, p.Code, p.Message)
		return
	}
	if err := command.Validate(rideID); err != nil {
		httpapi.WriteError(w, r, 400, "invalid_request", err.Error())
		return
	}
	if a.Trips == nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "Fleet trip effects are not configured")
		return
	}
	repo, ok := a.Store.(tripRepository)
	if !ok {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip store unavailable")
		return
	}
	var event TripEvent
	var trip Trip
	var err error
	if complete {
		event, trip, err = repo.PrepareTripCompletion(r.Context(), rideID, command)
	} else {
		event, trip, err = repo.PrepareTripStart(r.Context(), rideID, command)
	}
	if err != nil {
		writeTripError(w, r, err)
		return
	}
	if event.State == "accepted" {
		httpapi.WriteJSON(w, 200, trip)
		return
	}
	if event.State == "rejected" {
		httpapi.WriteError(w, r, 409, "trip_conflict", "Fleet rejected this trip event")
		return
	}
	result, err := a.Trips.Receipt(r.Context(), command.EventID)
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip effect remains pending reconciliation")
		return
	}
	if result.Status == http.StatusOK {
		if !validTripReceipt(result.Receipt, event.Effect, rideID, command) {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "Fleet receipt does not match the pending trip event")
			return
		}
		trip, err = repo.CommitTripEffect(r.Context(), command.EventID, result.Receipt)
		if err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip effect remains pending reconciliation")
			return
		}
		httpapi.WriteJSON(w, 200, trip)
		return
	}
	if result.Status != http.StatusNotFound {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "Fleet receipt could not be reconciled")
		return
	}
	result, err = postTripEffect(r.Context(), a.Trips, command, complete)
	if err != nil || result.Status >= 500 || result.Status == 0 {
		result, err = a.Trips.Receipt(r.Context(), command.EventID)
		if err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip effect remains pending reconciliation")
			return
		}
		if result.Status == http.StatusOK {
			if !validTripReceipt(result.Receipt, event.Effect, rideID, command) {
				httpapi.WriteError(w, r, 503, "temporarily_unavailable", "Fleet receipt does not match the pending trip event")
				return
			}
			trip, err = repo.CommitTripEffect(r.Context(), command.EventID, result.Receipt)
			if err != nil {
				httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip effect remains pending reconciliation")
				return
			}
			httpapi.WriteJSON(w, 200, trip)
			return
		}
		if result.Status == http.StatusNotFound {
			result, err = postTripEffect(r.Context(), a.Trips, command, complete)
			if err == nil && result.Status >= 200 && result.Status < 300 &&
				validTripReceipt(result.Receipt, event.Effect, rideID, command) {
				trip, err = repo.CommitTripEffect(r.Context(), command.EventID, result.Receipt)
				if err == nil {
					httpapi.WriteJSON(w, 200, trip)
					return
				}
			}
		}
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip effect remains pending reconciliation")
		return
	}
	if result.Status < 200 || result.Status >= 300 || !validTripReceipt(result.Receipt, event.Effect, rideID, command) {
		if result.Status >= 400 && result.Status < 500 {
			_ = repo.RejectTripEffect(r.Context(), command.EventID, result.Code)
			httpapi.WriteError(w, r, 409, "trip_conflict", "Fleet rejected this trip event")
			return
		}
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip effect remains pending reconciliation")
		return
	}
	trip, err = repo.CommitTripEffect(r.Context(), command.EventID, result.Receipt)
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip effect remains pending reconciliation")
		return
	}
	httpapi.WriteJSON(w, 200, trip)
}

func postTripEffect(ctx context.Context, effects TripEffects, command TripCommand, complete bool) (TripEffectResult, error) {
	if complete {
		return effects.Complete(ctx, fleetCompleteCommand(command))
	}
	return effects.Start(ctx, fleetStartCommand(command))
}

func fleetStartCommand(c TripCommand) FleetStartTripCommand {
	return FleetStartTripCommand{EventID: c.EventID, RunID: c.RunID, RideID: c.ReservationID,
		TripID: c.TripID, VehicleID: c.VehicleID, ReservationID: c.ReservationID,
		RouteID: c.RouteID, ExpectedVehicleVersion: c.ExpectedVehicleVersion}
}

func fleetCompleteCommand(c TripCommand) FleetCompleteTripCommand {
	return FleetCompleteTripCommand{EventID: c.EventID, RunID: c.RunID, RideID: c.ReservationID,
		TripID: c.TripID, VehicleID: c.VehicleID, ReservationID: c.ReservationID,
		RouteID: c.RouteID, ExpectedVehicleVersion: c.ExpectedVehicleVersion,
		DestinationZone: "centretown", DestinationPosition: TripPosition{Latitude: 45.42, Longitude: -75.693}}
}

func validTripReceipt(receipt FleetEffectReceipt, effect, rideID string, command TripCommand) bool {
	var expected string
	if effect == "trip_start" {
		expected, _, _ = payloadFingerprint(fleetStartCommand(command))
	} else {
		expected, _, _ = payloadFingerprint(fleetCompleteCommand(command))
	}
	wantEffect := effect
	return receipt.EventID == command.EventID && receipt.Effect == wantEffect && receipt.RunID == command.RunID &&
		receipt.RideID == rideID && receipt.TripID == command.TripID && receipt.VehicleID == command.VehicleID &&
		receipt.ReservationID == command.ReservationID && receipt.PayloadFingerprint == expected &&
		receipt.AcceptedVehicleVersion == command.ExpectedVehicleVersion+1 && !receipt.StoredAt.IsZero() &&
		(receipt.Result == "applied" || receipt.Result == "replayed")
}

func writeTripError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTripNotFound):
		httpapi.WriteError(w, r, 404, "not_found", "ride not found")
	case errors.Is(err, ErrTripConflict):
		httpapi.WriteError(w, r, 409, "trip_conflict", "trip command conflicts with current state")
	case errors.Is(err, ErrTripPending):
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "another trip effect is pending reconciliation")
	default:
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "trip store unavailable")
	}
}
