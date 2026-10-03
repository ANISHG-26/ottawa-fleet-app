package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecodeStrictRejectsDuplicateUnknownTrailingAndOversize(t *testing.T) {
	tests := []struct {
		name, body string
		size       int
		status     int
	}{
		{"duplicate", `{"name":"a","name":"b"}`, 0, 400},
		{"unknown", `{"name":"a","extra":true}`, 0, 400},
		{"wrong case", `{"Name":"a"}`, 0, 400},
		{"trailing", `{"name":"a"} {}`, 0, 400},
		{"oversized", `{"name":"` + strings.Repeat("x", 4090) + `"}`, 0, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			r.Header.Set("Content-Type", "application/json")
			var dst struct {
				Name string `json:"name"`
			}
			p := DecodeStrict(httptest.NewRecorder(), r, &dst)
			if p == nil || p.Status != tt.status {
				t.Fatalf("problem = %#v, want status %d", p, tt.status)
			}
		})
	}
}

func TestParsePageQueryRejectsMalformedEscapes(t *testing.T) {
	r := httptest.NewRequest("GET", "/?cursor=%GG", nil)
	if _, problem := ParsePageQuery(r); problem == nil || problem.Status != 400 {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestRequestIDInvalidCallerGetsGeneratedIDAnd400(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("invalid request reached handler") }))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Request-ID", "Bad ID")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("status %d", w.Code)
	}
	id := w.Header().Get("X-Request-ID")
	if len(id) < 3 || id != responseRequestID(t, w.Body.String()) {
		t.Fatalf("missing consistent generated request id: header=%q body=%s", id, w.Body.String())
	}
}

func responseRequestID(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatal(err)
	}
	return e.RequestID
}

func TestPaginationCursorBindsEndpointAndLimit(t *testing.T) {
	asOf := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	token := EncodeCursor("fleet", 2, asOf, "vehicle-002")
	gotTime, key, err := DecodeCursor(token, "fleet", 2)
	if err != nil || !gotTime.Equal(asOf) || key != "vehicle-002" {
		t.Fatalf("decode = %v %q %v", gotTime, key, err)
	}
	if _, _, err := DecodeCursor(token, "rides", 2); err == nil {
		t.Fatal("accepted cursor from another endpoint")
	}
	if _, _, err := DecodeCursor(token, "fleet", 3); err == nil {
		t.Fatal("accepted cursor with another limit")
	}
}
