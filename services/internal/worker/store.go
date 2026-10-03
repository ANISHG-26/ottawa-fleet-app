package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/ANISHG-26/ottawa-fleet-app/services/internal/ride"
)

type Job struct {
	ID                 string
	RideID             string
	RequestID          string
	Request            ride.Request
	Attempts           int
	Token              string
	ReconcileOnly      bool
	PendingFailureCode string
	Invalid            bool
}

type JobStore interface {
	Claim(context.Context, string) (*Job, error)
	Complete(context.Context, Job, string) (bool, error)
	Retry(context.Context, Job, time.Duration) (bool, error)
	Fail(context.Context, Job, string) (bool, error)
	DelayReconcile(context.Context, Job, time.Duration, string) (bool, error)
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

// Claim atomically selects one due job. Attempts count assignment POST attempts;
// a five-attempt job is leased for GET/DELETE reconciliation without incrementing.
func (s *PostgresStore) Claim(ctx context.Context, owner string) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id, rideID, requestID, payload, kind, version string
	var attempts int
	var reconcileOnly bool
	var pending sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT job_id,ride_id,request_id,payload::text,attempts,reconcile_only,pending_failure_code,kind,schema_version
		FROM assignment_jobs
		WHERE available_at<=clock_timestamp() AND (
			(state='queued' AND attempts<5) OR
			(state='processing' AND lease_expires_at<=clock_timestamp())
		)
		ORDER BY available_at,created_at,job_id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &rideID, &requestID, &payload, &attempts, &reconcileOnly, &pending, &kind, &version)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var req ride.Request
	decode := json.NewDecoder(bytes.NewBufferString(payload))
	decode.DisallowUnknownFields()
	decodeErr := decode.Decode(&req)
	if decodeErr == nil {
		var trailing any
		if decode.Decode(&trailing) != io.EOF {
			decodeErr = errors.New("trailing payload data")
		}
	}
	invalid := kind != "assign_ride" || version != "1.0.0" || decodeErr != nil || req.Validate() != nil
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	needsReconcile := reconcileOnly || attempts >= 5
	if !needsReconcile && attempts < 5 {
		attempts++
	}
	_, err = tx.ExecContext(ctx, `UPDATE assignment_jobs SET state='processing',attempts=$2,lease_owner=$3,lease_token=$4,
		lease_expires_at=clock_timestamp()+interval '30 seconds',available_at=clock_timestamp(),updated_at=GREATEST(updated_at,clock_timestamp()) WHERE job_id=$1`, id, attempts, owner, token)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE rides SET state='processing',updated_at=GREATEST(updated_at,clock_timestamp()) WHERE ride_id=$1`, rideID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	job := &Job{ID: id, RideID: rideID, RequestID: requestID, Request: req, Attempts: attempts, Token: token, ReconcileOnly: needsReconcile, Invalid: invalid}
	if pending.Valid {
		job.PendingFailureCode = pending.String
	}
	return job, nil
}

func (s *PostgresStore) Complete(ctx context.Context, j Job, vehicle string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE assignment_jobs SET state='completed',vehicle_id=$3,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=GREATEST(updated_at,clock_timestamp())
		WHERE job_id=$1 AND state='processing' AND lease_token=$2 AND lease_expires_at>clock_timestamp() AND attempts<=5`, j.ID, j.Token, vehicle)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE rides SET state='completed',vehicle_id=$2,failure_code=NULL,updated_at=GREATEST(updated_at,clock_timestamp()) WHERE ride_id=$1`, j.RideID, vehicle); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *PostgresStore) Retry(ctx context.Context, j Job, delay time.Duration) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE assignment_jobs SET state='queued',lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,
		available_at=clock_timestamp()+($3 * interval '1 second'),updated_at=GREATEST(updated_at,clock_timestamp())
		WHERE job_id=$1 AND state='processing' AND lease_token=$2 AND lease_expires_at>clock_timestamp() AND attempts<5`, j.ID, j.Token, delay.Seconds())
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE rides SET state='queued',updated_at=GREATEST(updated_at,clock_timestamp()) WHERE ride_id=$1`, j.RideID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *PostgresStore) Fail(ctx context.Context, j Job, code string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE assignment_jobs SET state='failed',failure_code=$3,lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=GREATEST(updated_at,clock_timestamp())
		WHERE job_id=$1 AND state='processing' AND lease_token=$2 AND lease_expires_at>clock_timestamp()`, j.ID, j.Token, code)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE rides SET state='failed',failure_code=$2,vehicle_id=NULL,updated_at=GREATEST(updated_at,clock_timestamp()) WHERE ride_id=$1`, j.RideID, code); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *PostgresStore) DelayReconcile(ctx context.Context, j Job, delay time.Duration, code string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE assignment_jobs SET reconcile_only=true,pending_failure_code=COALESCE(pending_failure_code,$4),lease_expires_at=clock_timestamp()+($3 * interval '1 second'),available_at=clock_timestamp()+($3 * interval '1 second'),updated_at=GREATEST(updated_at,clock_timestamp())
		WHERE job_id=$1 AND state='processing' AND lease_token=$2 AND lease_expires_at>clock_timestamp()`, j.ID, j.Token, delay.Seconds(), nullableCode(code))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func nullableCode(code string) any {
	if code == "" {
		return nil
	}
	return code
}

func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(b[:]), nil
}
