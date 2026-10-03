-- Stable across process restarts, rotated by a named local dataset reset.
CREATE TABLE IF NOT EXISTS app_runtime_state (
    singleton smallint PRIMARY KEY CHECK (singleton = 1),
    dataset_epoch text NOT NULL CHECK (dataset_epoch ~ '^[0-9a-f]{32}$'),
    updated_at timestamptz NOT NULL DEFAULT now()
);
