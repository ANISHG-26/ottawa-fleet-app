package ride

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/buildinfo"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/httpapi"
)

var idemPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type API struct {
	Store Repository
	DB    *sql.DB
}
type Repository interface {
	Submit(context.Context, string, string, Request) (Submission, Ride, error)
	Get(context.Context, string) (Ride, error)
	List(context.Context, int, *time.Time, *time.Time, string) (Page, error)
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/buildinfo", func(w http.ResponseWriter, r *http.Request) { httpapi.WriteJSON(w, http.StatusOK, buildinfo.Current()) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if a.DB == nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "database migrations are unavailable or incompatible")
			return
		}
		var compatible bool
		_, epochErr := database.DatasetEpoch(ctx, a.DB)
		if a.DB.PingContext(ctx) != nil || a.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM app_schema_migrations WHERE name='ride/001_ride_jobs.sql')`).Scan(&compatible) != nil || !compatible || epochErr != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "database migrations are unavailable or incompatible")
			return
		}
		httpapi.WriteJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/metrics", a.metrics)
	mux.HandleFunc("/v1/rides", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rides" {
			httpapi.WriteError(w, r, 404, "not_found", "ride not found")
			return
		}
		switch r.Method {
		case http.MethodPost:
			a.submit(w, r)
		case http.MethodGet:
			a.list(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			httpapi.WriteError(w, r, 405, "invalid_request", "method not allowed")
		}
	})
	mux.HandleFunc("/v1/rides/", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[len("/v1/rides/"):]
		if id == "" || len(id) > 64 || !regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`).MatchString(id) {
			httpapi.WriteError(w, r, 400, "invalid_request", "invalid ride ID")
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			httpapi.WriteError(w, r, 405, "invalid_request", "method not allowed")
			return
		}
		ride, err := a.Store.Get(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			httpapi.WriteError(w, r, 404, "not_found", "ride not found")
			return
		}
		if err != nil {
			httpapi.WriteError(w, r, 503, "temporarily_unavailable", "ride store unavailable")
			return
		}
		httpapi.WriteJSON(w, 200, ride)
	})
	return httpapi.RequestID(mux)
}

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if !idemPattern.MatchString(key) {
		httpapi.WriteError(w, r, 400, "invalid_request", "Idempotency-Key must contain 8 to 128 ASCII letters, digits, underscores or hyphens")
		return
	}
	var request Request
	if p := httpapi.DecodeStrict(w, r, &request); p != nil {
		httpapi.WriteError(w, r, p.Status, p.Code, p.Message)
		return
	}
	if err := request.Validate(); err != nil {
		httpapi.WriteError(w, r, 400, "invalid_request", err.Error())
		return
	}
	requestID := httpapi.RequestIDFromContext(r.Context())
	created, ride, err := a.Store.Submit(r.Context(), key, requestID, request)
	if IsConflict(err) {
		httpapi.WriteError(w, r, 409, "idempotency_conflict", "idempotency key was already used with different ride fields")
		return
	}
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "ride could not be durably accepted")
		return
	}
	_ = created // Both acceptance and an equal replay return the current representation.
	w.Header().Set("Location", "/v1/rides/"+ride.RideID)
	httpapi.WriteJSON(w, 202, ride)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	query, p := httpapi.ParsePageQuery(r)
	if p != nil {
		httpapi.WriteError(w, r, p.Status, p.Code, p.Message)
		return
	}
	epoch, err := database.DatasetEpoch(r.Context(), a.DB)
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "ride pagination metadata unavailable")
		return
	}
	endpoint := "rides:" + epoch
	var asOf, lastTime *time.Time
	lastID := ""
	if query.Cursor != "" {
		decoded, key, err := httpapi.DecodeCursor(query.Cursor, endpoint, query.Limit)
		if err != nil {
			httpapi.WriteError(w, r, 400, "invalid_request", "invalid or foreign cursor")
			return
		}
		asOf = &decoded
		// The cursor's last key contains the RFC3339Nano timestamp followed by a NUL and ride ID.
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 || !regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`).MatchString(parts[1]) {
			httpapi.WriteError(w, r, 400, "invalid_request", "invalid cursor key")
			return
		}
		at, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			httpapi.WriteError(w, r, 400, "invalid_request", "invalid cursor key")
			return
		}
		lastTime = &at
		lastID = parts[1]
	}
	page, err := a.Store.List(r.Context(), query.Limit, asOf, lastTime, lastID)
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "ride list unavailable")
		return
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		cursor := httpapi.EncodeCursor(endpoint, query.Limit, page.AsOf, last.CreatedAt.UTC().Format(time.RFC3339Nano)+"\x00"+last.RideID)
		page.NextCursor = &cursor
	}
	page.HasMore = false
	httpapi.WriteJSON(w, 200, page)
}

func (a *API) metrics(w http.ResponseWriter, r *http.Request) {
	var pending int
	err := a.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM assignment_jobs WHERE state IN ('queued','processing')`).Scan(&pending)
	if err != nil {
		httpapi.WriteError(w, r, 503, "temporarily_unavailable", "metrics unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte("ride_pending_jobs " + strconv.Itoa(pending) + "\n"))
}
