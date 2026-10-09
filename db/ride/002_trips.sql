CREATE TABLE ride_trips (
    ride_id text PRIMARY KEY REFERENCES rides(ride_id),
    trip_id text NOT NULL UNIQUE,
    run_id text NOT NULL,
    route_id text NOT NULL CHECK (route_id = 'lansdowne-centretown-v1'),
    vehicle_id text NOT NULL,
    reservation_id text NOT NULL,
    start_zone text NOT NULL,
    destination_zone text NOT NULL,
    state text NOT NULL CHECK (state IN ('not_started','in_progress','completed','cancelled')),
    version integer NOT NULL CHECK (version >= 1),
    vehicle_version integer NOT NULL CHECK (vehicle_version >= 1),
    started_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((state = 'not_started' AND started_at IS NULL AND completed_at IS NULL)
        OR (state = 'in_progress' AND started_at IS NOT NULL AND completed_at IS NULL)
        OR (state = 'completed' AND started_at IS NOT NULL AND completed_at IS NOT NULL)
        OR (state = 'cancelled' AND completed_at IS NULL))
);

CREATE TABLE ride_trip_effect_events (
    event_id text PRIMARY KEY,
    ride_id text NOT NULL REFERENCES rides(ride_id),
    trip_id text NOT NULL,
    effect text NOT NULL CHECK (effect IN ('trip_start','trip_complete')),
    normalized_payload jsonb NOT NULL,
    payload_fingerprint char(64) NOT NULL CHECK (payload_fingerprint ~ '^[a-f0-9]{64}$'),
    state text NOT NULL CHECK (state IN ('pending','accepted','rejected')),
    receipt jsonb,
    error_code text,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((state = 'accepted' AND receipt IS NOT NULL AND error_code IS NULL)
        OR (state = 'rejected' AND receipt IS NULL AND error_code IS NOT NULL)
        OR (state = 'pending' AND receipt IS NULL AND error_code IS NULL))
);

CREATE UNIQUE INDEX ride_trip_one_pending_effect_idx
    ON ride_trip_effect_events(ride_id) WHERE state = 'pending';
