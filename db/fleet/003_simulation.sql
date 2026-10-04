ALTER TABLE fleet_vehicles
    ADD COLUMN vehicle_version bigint NOT NULL DEFAULT 1 CHECK (vehicle_version > 0),
    ADD COLUMN operational_state text NOT NULL DEFAULT 'available'
        CHECK (operational_state IN ('available','reserved','on_trip','out_of_service')),
    ADD COLUMN active_trip_id text,
    ADD COLUMN active_ride_id text,
    ADD COLUMN active_run_id text,
    ADD COLUMN trip_started_at timestamptz,
    ADD COLUMN position_latitude double precision,
    ADD COLUMN position_longitude double precision,
    ADD COLUMN route_id text,
    ADD COLUMN route_version integer,
    ADD COLUMN route_segment integer,
    ADD COLUMN segment_start_latitude double precision,
    ADD COLUMN segment_start_longitude double precision,
    ADD COLUMN segment_end_latitude double precision,
    ADD COLUMN segment_end_longitude double precision,
    ADD COLUMN segment_started_at timestamptz,
    ADD COLUMN segment_ends_at timestamptz,
    ADD CONSTRAINT fleet_vehicle_active_trip_consistency CHECK (
        (operational_state = 'on_trip' AND active_trip_id IS NOT NULL AND active_ride_id IS NOT NULL
            AND active_run_id IS NOT NULL AND route_id IS NOT NULL AND route_version IS NOT NULL)
        OR (operational_state <> 'on_trip' AND active_trip_id IS NULL AND active_ride_id IS NULL AND active_run_id IS NULL)
    );

-- Existing v1 reservations remain authoritative and keep their behavior.
-- This column only reflects that ownership for v2 operational reads.
UPDATE fleet_vehicles v SET operational_state='reserved'
WHERE EXISTS (SELECT 1 FROM fleet_reservations r WHERE r.vehicle_id=v.vehicle_id AND r.state='reserved');

CREATE TABLE fleet_simulation_effects (
    event_id text PRIMARY KEY CHECK (event_id ~ '^[a-z][a-z0-9-]{2,63}$'),
    effect text NOT NULL CHECK (effect IN ('trip_start','trip_complete','position_observation')),
    run_id text NOT NULL CHECK (run_id ~ '^[a-z][a-z0-9-]{2,63}$'),
    ride_id text NOT NULL CHECK (ride_id ~ '^[a-z][a-z0-9-]{2,63}$'),
    trip_id text NOT NULL CHECK (trip_id ~ '^[a-z][a-z0-9-]{2,63}$'),
    vehicle_id text NOT NULL REFERENCES fleet_vehicles(vehicle_id),
    reservation_id text NOT NULL,
    payload_fingerprint char(64) NOT NULL CHECK (payload_fingerprint ~ '^[a-f0-9]{64}$'),
    accepted_vehicle_version bigint NOT NULL CHECK (accepted_vehicle_version > 0),
    stored_at timestamptz NOT NULL,
    receipt jsonb NOT NULL
);

CREATE UNIQUE INDEX fleet_trip_start_effect_once ON fleet_simulation_effects(trip_id)
    WHERE effect='trip_start';
CREATE UNIQUE INDEX fleet_trip_complete_effect_once ON fleet_simulation_effects(trip_id)
    WHERE effect='trip_complete';
CREATE INDEX fleet_simulation_effects_vehicle_time ON fleet_simulation_effects(vehicle_id,stored_at DESC);

CREATE TABLE fleet_inventory_profile (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    profile text NOT NULL CHECK (profile IN ('route20synthetic')),
    initialized_at timestamptz NOT NULL
);
