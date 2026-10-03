package ride

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Request struct {
	PickupZone  string `json:"pickup_zone"`
	DropoffZone string `json:"dropoff_zone"`
	Passengers  int    `json:"passengers"`
}

func (r Request) Validate() error {
	if !validZone(r.PickupZone) || !validZone(r.DropoffZone) {
		return errors.New("pickup_zone and dropoff_zone must be supported zones")
	}
	if r.Passengers < 1 || r.Passengers > 4 {
		return errors.New("passengers must be between 1 and 4")
	}
	return nil
}

type Ride struct {
	RideID      string    `json:"ride_id"`
	PickupZone  string    `json:"pickup_zone"`
	DropoffZone string    `json:"dropoff_zone"`
	Passengers  int       `json:"passengers"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	VehicleID   *string   `json:"vehicle_id,omitempty"`
	FailureCode *string   `json:"failure_code,omitempty"`
}

type Page struct {
	Items      []Ride    `json:"items"`
	NextCursor *string   `json:"next_cursor"`
	AsOf       time.Time `json:"as_of"`
	HasMore    bool      `json:"-"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

type Submission struct{ Created bool }
type ConflictError struct{}

func (*ConflictError) Error() string {
	return "idempotency key already identifies different ride fields"
}
func IsConflict(err error) bool { var target *ConflictError; return errors.As(err, &target) }

func (s *Store) Submit(ctx context.Context, key, requestID string, request Request) (Submission, Ride, error) {
	if err := request.Validate(); err != nil {
		return Submission{}, Ride{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Submission{}, Ride{}, err
	}
	defer tx.Rollback()
	rideID, err := newID("ride-")
	if err != nil {
		return Submission{}, Ride{}, err
	}
	jobID, err := newID("job-")
	if err != nil {
		return Submission{}, Ride{}, err
	}
	var accepted Ride
	err = tx.QueryRowContext(ctx, `INSERT INTO rides(ride_id,pickup_zone,dropoff_zone,passengers,state)
		VALUES($1,$2,$3,$4,'queued') RETURNING ride_id,pickup_zone,dropoff_zone,passengers,state,created_at,updated_at`,
		rideID, request.PickupZone, request.DropoffZone, request.Passengers).Scan(&accepted.RideID, &accepted.PickupZone, &accepted.DropoffZone, &accepted.Passengers, &accepted.State, &accepted.CreatedAt, &accepted.UpdatedAt)
	if err != nil {
		return Submission{}, Ride{}, err
	}
	payload, _ := json.Marshal(request)
	_, err = tx.ExecContext(ctx, `INSERT INTO assignment_jobs(job_id,ride_id,request_id,kind,payload,state) VALUES($1,$2,$3,'assign_ride',$4,'queued')`, jobID, rideID, requestID, payload)
	if err != nil {
		return Submission{}, Ride{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO ride_idempotency(idempotency_key,ride_id,pickup_zone,dropoff_zone,passengers)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(idempotency_key) DO NOTHING`, key, rideID, request.PickupZone, request.DropoffZone, request.Passengers)
	if err != nil {
		return Submission{}, Ride{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Submission{}, Ride{}, err
	}
	if inserted == 0 {
		_ = tx.Rollback()
		prior, e := s.getByKey(ctx, key)
		if e != nil {
			return Submission{}, Ride{}, e
		}
		if prior.PickupZone != request.PickupZone || prior.DropoffZone != request.DropoffZone || prior.Passengers != request.Passengers {
			return Submission{}, Ride{}, &ConflictError{}
		}
		return Submission{Created: false}, prior, nil
	}
	if err = tx.Commit(); err != nil {
		return Submission{}, Ride{}, err
	}
	normalizeRide(&accepted)
	return Submission{Created: true}, accepted, nil
}

func (s *Store) getByKey(ctx context.Context, key string) (Ride, error) {
	var r Ride
	err := s.db.QueryRowContext(ctx, `SELECT r.ride_id,r.pickup_zone,r.dropoff_zone,r.passengers,r.state,r.created_at,r.updated_at,r.vehicle_id,r.failure_code
		FROM ride_idempotency i JOIN rides r USING(ride_id) WHERE i.idempotency_key=$1`, key).Scan(&r.RideID, &r.PickupZone, &r.DropoffZone, &r.Passengers, &r.State, &r.CreatedAt, &r.UpdatedAt, &r.VehicleID, &r.FailureCode)
	normalizeRide(&r)
	return r, err
}

func (s *Store) Get(ctx context.Context, id string) (Ride, error) {
	var r Ride
	err := s.db.QueryRowContext(ctx, `SELECT ride_id,pickup_zone,dropoff_zone,passengers,state,created_at,updated_at,vehicle_id,failure_code FROM rides WHERE ride_id=$1`, id).
		Scan(&r.RideID, &r.PickupZone, &r.DropoffZone, &r.Passengers, &r.State, &r.CreatedAt, &r.UpdatedAt, &r.VehicleID, &r.FailureCode)
	normalizeRide(&r)
	return r, err
}

func (s *Store) List(ctx context.Context, limit int, asOf *time.Time, lastCreated *time.Time, lastID string) (Page, error) {
	page := Page{Items: []Ride{}}
	if asOf == nil {
		if err := s.db.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&page.AsOf); err != nil {
			return Page{}, err
		}
	} else {
		page.AsOf = *asOf
	}
	query := `SELECT ride_id,pickup_zone,dropoff_zone,passengers,state,created_at,updated_at,vehicle_id,failure_code FROM rides WHERE created_at <= $1`
	args := []any{page.AsOf}
	if lastCreated != nil {
		query += ` AND (created_at,ride_id)>($2,$3)`
		args = append(args, *lastCreated, lastID)
	}
	query += fmt.Sprintf(` ORDER BY created_at,ride_id LIMIT %d`, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Ride
		if err := rows.Scan(&r.RideID, &r.PickupZone, &r.DropoffZone, &r.Passengers, &r.State, &r.CreatedAt, &r.UpdatedAt, &r.VehicleID, &r.FailureCode); err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > limit {
		page.HasMore = true
		page.Items = page.Items[:limit]
	}
	for i := range page.Items {
		normalizeRide(&page.Items[i])
	}
	page.AsOf = page.AsOf.UTC()
	return page, nil
}

func validZone(z string) bool {
	switch z {
	case "centretown", "glebe", "lansdowne", "byward-market":
		return true
	}
	return false
}
func newID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
func normalizeRide(r *Ride) { r.CreatedAt = r.CreatedAt.UTC(); r.UpdatedAt = r.UpdatedAt.UTC() }
