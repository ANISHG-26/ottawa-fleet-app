CREATE TABLE IF NOT EXISTS fleet_vehicles (
    vehicle_id text PRIMARY KEY,
    zone text NOT NULL CHECK (zone IN ('centretown','glebe','lansdowne','byward-market')),
    seats integer NOT NULL CHECK (seats BETWEEN 1 AND 4),
    availability text NOT NULL CHECK (availability IN ('available','unavailable')),
    observed_at timestamptz NOT NULL,
    maintenance boolean NOT NULL DEFAULT false,
    battery_percent integer NOT NULL DEFAULT 100 CHECK (battery_percent BETWEEN 0 AND 100)
);

CREATE TABLE IF NOT EXISTS fleet_reservations (
    ride_id text PRIMARY KEY,
    vehicle_id text REFERENCES fleet_vehicles(vehicle_id),
    state text NOT NULL CHECK (state IN ('reserved','released')),
    reserved_at timestamptz,
    updated_at timestamptz NOT NULL,
    pickup_zone text,
    passengers integer,
    CHECK ((state='reserved' AND vehicle_id IS NOT NULL AND reserved_at IS NOT NULL AND pickup_zone IS NOT NULL AND passengers BETWEEN 1 AND 4)
        OR (state='released' AND ((vehicle_id IS NULL AND reserved_at IS NULL) OR (vehicle_id IS NOT NULL AND reserved_at IS NOT NULL))))
);

CREATE UNIQUE INDEX IF NOT EXISTS fleet_one_active_reservation_per_vehicle
    ON fleet_reservations(vehicle_id) WHERE state='reserved';

CREATE INDEX IF NOT EXISTS fleet_inventory_order ON fleet_vehicles(vehicle_id);
