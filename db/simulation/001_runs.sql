CREATE TABLE simulation_runs (
    run_id text PRIMARY KEY CHECK (run_id ~ '^[a-z][a-z0-9-]{2,63}$'),
    idempotency_key text NOT NULL UNIQUE,
    manifest_fingerprint char(64) NOT NULL,
    manifest jsonb NOT NULL,
    state text NOT NULL CHECK (state IN ('scheduled','running','stopping','completed','stopped','failed')),
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    deadline_at timestamptz NOT NULL,
    stop_requested_at timestamptz,
    drain_deadline_at timestamptz NOT NULL,
    completed_at timestamptz,
    terminal_reason text,
    issued_events integer NOT NULL DEFAULT 0,
    completed_events integer NOT NULL DEFAULT 0,
    incomplete_trips integer NOT NULL DEFAULT 0,
    incomplete_requests integer NOT NULL DEFAULT 0,
    incomplete_events integer NOT NULL DEFAULT 0,
    version bigint NOT NULL DEFAULT 1,
    CHECK (issued_events BETWEEN 0 AND 60 AND completed_events BETWEEN 0 AND 60),
    CHECK (incomplete_trips BETWEEN 0 AND 20 AND incomplete_requests BETWEEN 0 AND 20 AND incomplete_events BETWEEN 0 AND 60)
);
CREATE UNIQUE INDEX simulation_one_active_run ON simulation_runs((true)) WHERE state IN ('scheduled','running','stopping');

CREATE TABLE simulation_requests (
    run_id text NOT NULL REFERENCES simulation_runs(run_id),
    sequence integer NOT NULL CHECK (sequence BETWEEN 1 AND 20),
    ride_id text UNIQUE,
    idempotency_key text NOT NULL UNIQUE,
    trip_id text NOT NULL UNIQUE,
    scheduled_at timestamptz NOT NULL,
    submitted_at timestamptz,
    assignment_state text NOT NULL DEFAULT 'pending' CHECK (assignment_state IN ('pending','accepted','assigned','failed','unfinished')),
    trip_state text NOT NULL DEFAULT 'not_started' CHECK (trip_state IN ('not_started','in_progress','completed','cancelled','unfinished')),
    vehicle_id text,
    PRIMARY KEY(run_id,sequence)
);

CREATE TABLE simulation_events (
    event_id text PRIMARY KEY CHECK (event_id ~ '^[a-z][a-z0-9-]{2,63}$'),
    run_id text NOT NULL REFERENCES simulation_runs(run_id),
    sequence integer NOT NULL CHECK (sequence BETWEEN 1 AND 60),
    request_sequence integer NOT NULL CHECK (request_sequence BETWEEN 1 AND 20),
    kind text NOT NULL CHECK (kind IN ('trip_start','trip_complete')),
    ride_id text,
    trip_id text NOT NULL,
    vehicle_id text,
    expected_trip_version bigint,
    expected_vehicle_version bigint,
    scheduled_at timestamptz NOT NULL,
    issued_at timestamptz,
    result text NOT NULL DEFAULT 'pending' CHECK (result IN ('pending','applied','replayed','rejected','failed')),
    payload jsonb,
    UNIQUE(run_id,sequence),
    UNIQUE(run_id,request_sequence,kind),
    FOREIGN KEY(run_id,request_sequence) REFERENCES simulation_requests(run_id,sequence)
);
CREATE INDEX simulation_events_due ON simulation_events(run_id,scheduled_at,sequence) WHERE result='pending';

-- The event ledger is authoritative: counters change in the same transaction
-- as receipt/payload state, including when a dispatcher crashes and replays.
CREATE FUNCTION simulation_sync_run_counters() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_run text;
BEGIN
    target_run := COALESCE(NEW.run_id, OLD.run_id);
    UPDATE simulation_runs SET
        issued_events=(SELECT count(*) FROM simulation_events WHERE run_id=target_run AND issued_at IS NOT NULL),
        completed_events=(SELECT count(*) FROM simulation_events WHERE run_id=target_run AND result IN ('applied','replayed')),
        version=version+1
    WHERE run_id=target_run;
    RETURN NEW;
END $$;
CREATE TRIGGER simulation_event_counters AFTER INSERT OR UPDATE OF issued_at,result ON simulation_events
    FOR EACH ROW EXECUTE FUNCTION simulation_sync_run_counters();
