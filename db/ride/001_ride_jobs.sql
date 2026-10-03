CREATE TABLE rides (
    ride_id text PRIMARY KEY,
    pickup_zone text NOT NULL CHECK (pickup_zone IN ('centretown','glebe','lansdowne','byward-market')),
    dropoff_zone text NOT NULL CHECK (dropoff_zone IN ('centretown','glebe','lansdowne','byward-market')),
    passengers integer NOT NULL CHECK (passengers BETWEEN 1 AND 4),
    state text NOT NULL CHECK (state IN ('queued','processing','completed','failed')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    vehicle_id text,
    failure_code text,
    CHECK ((state = 'completed' AND vehicle_id IS NOT NULL AND failure_code IS NULL)
        OR (state = 'failed' AND failure_code IS NOT NULL AND vehicle_id IS NULL)
        OR (state IN ('queued','processing') AND vehicle_id IS NULL AND failure_code IS NULL))
);

CREATE TABLE ride_idempotency (
    idempotency_key text PRIMARY KEY,
    ride_id text NOT NULL UNIQUE REFERENCES rides(ride_id),
    pickup_zone text NOT NULL,
    dropoff_zone text NOT NULL,
    passengers integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE assignment_jobs (
    job_id text PRIMARY KEY,
    ride_id text NOT NULL UNIQUE REFERENCES rides(ride_id),
    request_id text NOT NULL,
    schema_version text NOT NULL DEFAULT '1.0.0',
    kind text NOT NULL,
    payload jsonb NOT NULL,
    state text NOT NULL CHECK (state IN ('queued','processing','completed','failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts = 5),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_owner text,
    lease_token text,
    lease_expires_at timestamptz,
    reconcile_only boolean NOT NULL DEFAULT false,
    pending_failure_code text,
    vehicle_id text,
    failure_code text,
    CHECK ((state = 'processing' AND lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (state <> 'processing' AND lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL)),
    CHECK (state <> 'queued' OR attempts <= 4),
    CHECK ((state = 'completed' AND vehicle_id IS NOT NULL AND failure_code IS NULL)
        OR (state = 'failed' AND failure_code IS NOT NULL AND vehicle_id IS NULL)
        OR (state IN ('queued','processing') AND vehicle_id IS NULL AND failure_code IS NULL))
);

CREATE INDEX assignment_jobs_due_idx ON assignment_jobs (available_at, created_at, job_id) WHERE state IN ('queued','processing');
CREATE INDEX rides_page_idx ON rides (created_at, ride_id);
