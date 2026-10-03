package ride

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/database"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type stubRepository struct{ submitCalls int }

func (s *stubRepository) Submit(_ context.Context, key, requestID string, req Request) (Submission, Ride, error) {
	s.submitCalls++
	return Submission{Created: true}, Ride{RideID: "ride-test-001", PickupZone: req.PickupZone, DropoffZone: req.DropoffZone, Passengers: req.Passengers, State: "queued", CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}, nil
}

func TestRideCursorIsBoundToDatasetEpoch(t *testing.T) {
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	db, cleanup := isolatedRideDB(t, raw)
	defer cleanup()
	ctx := context.Background()
	epochDDL, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "fleet", "002_dataset_epoch.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(epochDDL)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.EnsureDatasetEpoch(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("cursor_key_%04d", i)
		if _, _, err = store.Submit(ctx, key, "req-cursor-test", Request{PickupZone: "glebe", DropoffZone: "glebe", Passengers: 1}); err != nil {
			t.Fatal(err)
		}
	}
	handler := (&API{Store: store, DB: db}).Handler()
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/rides?limit=1", nil))
	if first.Code != 200 {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	var page Page
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == nil || *page.NextCursor == "" {
		t.Fatalf("expected a next cursor: %+v", page)
	}
	if _, err = database.RotateDatasetEpoch(ctx, db); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/rides?limit=1&cursor="+url.QueryEscape(*page.NextCursor), nil)
	next := httptest.NewRecorder()
	handler.ServeHTTP(next, req)
	if next.Code != 400 {
		t.Fatalf("cursor remained valid after dataset reset: status=%d body=%s", next.Code, next.Body.String())
	}
}
func (s *stubRepository) Get(context.Context, string) (Ride, error) { return Ride{}, nil }
func (s *stubRepository) List(context.Context, int, *time.Time, *time.Time, string) (Page, error) {
	return Page{Items: []Ride{}, AsOf: time.Now().UTC()}, nil
}

func TestSubmitStrictContractRejections(t *testing.T) {
	cases := []struct {
		name, contentType, key, body string
		want                         int
	}{
		{"missing key", "application/json", "", "{}", 400},
		{"unknown field", "application/json", "scenario_0001", "{\"pickup_zone\":\"lansdowne\",\"dropoff_zone\":\"centretown\",\"passengers\":2,\"extra\":true}", 400},
		{"duplicate field", "application/json", "scenario_0001", "{\"pickup_zone\":\"lansdowne\",\"pickup_zone\":\"glebe\",\"dropoff_zone\":\"centretown\",\"passengers\":2}", 400},
		{"unsupported media type", "text/plain", "scenario_0001", "{}", 415},
		{"oversized body", "application/json", "scenario_0001", `{"padding":"` + strings.Repeat("x", 4100) + `"}`, 413},
		{"invalid field", "application/json", "scenario_0001", "{\"pickup_zone\":\"mars\",\"dropoff_zone\":\"centretown\",\"passengers\":2}", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubRepository{}
			handler := (&API{Store: repo}).Handler()
			req := httptest.NewRequest(http.MethodPost, "/v1/rides", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status=%d, body=%s, want %d", res.Code, res.Body.String(), tc.want)
			}
			if res.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing X-Request-ID")
			}
			if repo.submitCalls != 0 {
				t.Fatal("invalid request reached persistence")
			}
		})
	}
}

func TestSubmitReturnsLocationOnlyAfterAcceptedRepositoryResult(t *testing.T) {
	repo := &stubRepository{}
	handler := (&API{Store: repo}).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/rides", strings.NewReader(`{"pickup_zone":"lansdowne","dropoff_zone":"centretown","passengers":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "scenario_0001")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != 202 {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if res.Header().Get("Location") != "/v1/rides/ride-test-001" {
		t.Fatalf("location=%q", res.Header().Get("Location"))
	}
	if repo.submitCalls != 1 {
		t.Fatalf("submit calls=%d", repo.submitCalls)
	}
	if !strings.Contains(res.Body.String(), "\"created_at\":\"2026-10-03T12:00:00Z\"") {
		t.Fatalf("timestamp not canonical UTC: %s", res.Body.String())
	}
}
