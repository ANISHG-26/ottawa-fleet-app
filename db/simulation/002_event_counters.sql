CREATE OR REPLACE FUNCTION simulation_sync_run_counters() RETURNS trigger LANGUAGE plpgsql AS $$
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
DROP TRIGGER IF EXISTS simulation_event_counters ON simulation_events;
CREATE TRIGGER simulation_event_counters AFTER INSERT OR UPDATE OF issued_at,result ON simulation_events
    FOR EACH ROW EXECUTE FUNCTION simulation_sync_run_counters();
