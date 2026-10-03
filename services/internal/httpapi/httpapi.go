package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxJSONBody = 4096

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`)

type contextKey struct{}

type Problem struct {
	Status  int
	Code    string
	Message string
}
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-generated"
	}
	return "req-" + hex.EncodeToString(b[:])
}
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newID()
		}
		if r.Header.Get("X-Request-ID") != "" && !idPattern.MatchString(id) {
			id = newID()
			w.Header().Set("X-Request-ID", id)
			writeErrorWithID(w, 400, "invalid_request", "X-Request-ID is invalid", id)
			return
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), contextKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeErrorWithID(w, status, code, message, RequestIDFromContext(r.Context()))
}
func writeErrorWithID(w http.ResponseWriter, status int, code, message, id string) {
	WriteJSON(w, status, ErrorBody{Code: code, Message: message, RequestID: id})
}

func DecodeStrict(_ http.ResponseWriter, r *http.Request, dst any) *Problem {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return &Problem{415, "invalid_request", "Content-Type must be application/json"}
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxJSONBody+1))
	if err != nil {
		return &Problem{400, "invalid_request", "request body could not be read"}
	}
	if len(data) > MaxJSONBody {
		return &Problem{413, "invalid_request", "request body exceeds 4096 bytes"}
	}
	if !utf8.Valid(data) {
		return &Problem{400, "invalid_request", "request body must be valid UTF-8"}
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return &Problem{400, "invalid_request", "request body contains invalid or duplicate JSON fields"}
	}
	if err := rejectNonExactFields(data, dst); err != nil {
		return &Problem{400, "invalid_request", "request body contains an unknown field"}
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return &Problem{400, "invalid_request", "request body is invalid"}
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return &Problem{400, "invalid_request", "request body must contain exactly one JSON value"}
	}
	return nil
}

// encoding/json accepts case-insensitive struct keys. Contracts require exact
// JSON names, so check keys before decoding into API request structs.
func rejectNonExactFields(data []byte, dst any) error {
	typeOf := reflect.TypeOf(dst)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		return nil
	}
	typeOf = typeOf.Elem()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if object == nil {
		return nil
	}
	allowed := map[string]bool{}
	for i := 0; i < typeOf.NumField(); i++ {
		field := typeOf.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if name != "-" {
			allowed[name] = true
		}
	}
	for name := range object {
		if !allowed[name] {
			return errors.New("non-exact or unknown JSON field")
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	if err := scanValue(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func scanValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			kt, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := kt.(string)
			if !ok || seen[key] {
				return io.ErrUnexpectedEOF
			}
			seen[key] = true
			if err := scanValue(d); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return io.ErrUnexpectedEOF
		}
	case json.Delim('['):
		for d.More() {
			if err := scanValue(d); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return io.ErrUnexpectedEOF
		}
	case json.Delim('}'), json.Delim(']'):
		return io.ErrUnexpectedEOF
	}
	return nil
}

type PageQuery struct {
	Limit  int
	Cursor string
}

func ParsePageQuery(r *http.Request) (PageQuery, *Problem) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return PageQuery{}, &Problem{400, "invalid_request", "query is malformed"}
	}
	for key, vv := range values {
		if key != "limit" && key != "cursor" || len(vv) != 1 {
			return PageQuery{}, &Problem{400, "invalid_request", "query contains an unknown or repeated parameter"}
		}
	}
	limit := 20
	if raw := values.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return PageQuery{}, &Problem{400, "invalid_request", "limit must be an integer from 1 to 100"}
		}
		limit = n
	} else if _, ok := values["limit"]; ok {
		return PageQuery{}, &Problem{400, "invalid_request", "limit must be an integer from 1 to 100"}
	}
	cursor := values.Get("cursor")
	if len(cursor) > 256 || (cursor == "" && values.Has("cursor")) {
		return PageQuery{}, &Problem{400, "invalid_request", "cursor is invalid"}
	}
	return PageQuery{Limit: limit, Cursor: cursor}, nil
}

// Cursor helpers are intentionally opaque and validate endpoint/limit binding.
// Values are base64url JSON, with no user-controlled query syntax.
type cursorData struct {
	Endpoint string `json:"e"`
	Limit    int    `json:"l"`
	AsOf     string `json:"a"`
	Last     string `json:"k"`
}

func EncodeCursor(endpoint string, limit int, asOf time.Time, lastKey string) string {
	b, _ := json.Marshal(cursorData{endpoint, limit, asOf.UTC().Format(time.RFC3339Nano), lastKey})
	return base64.RawURLEncoding.EncodeToString(b)
}
func DecodeCursor(cursor, endpoint string, limit int) (time.Time, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(b) > 512 {
		return time.Time{}, "", errors.New("invalid cursor")
	}
	var data cursorData
	if json.Unmarshal(b, &data) != nil || data.Endpoint != endpoint || data.Limit != limit || data.Last == "" || !strings.HasSuffix(data.AsOf, "Z") {
		return time.Time{}, "", errors.New("invalid cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, data.AsOf)
	if err != nil || t.Location() != time.UTC {
		return time.Time{}, "", errors.New("invalid cursor")
	}
	return t.UTC(), data.Last, nil
}
