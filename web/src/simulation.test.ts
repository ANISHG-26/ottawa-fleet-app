import { describe, expect, it, vi } from 'vitest';
import { getPosition, interpolatePosition, mapLimit, scenarioManifest, SIMULATION_LIMITS, stopRun, type Position } from './simulation';

const settings = { requestCount: 2, rate: 1, durationSeconds: 10, concurrency: 2, seed: 42 };
const sample = (overrides: Partial<Position> = {}): Position => ({ vehicle_id: 'vehicle-001', position: { latitude: 45.4, longitude: -75.6 }, observed_at: '2026-10-03T12:00:00Z', as_of: '2026-10-03T12:00:00Z', freshness: 'fresh', operational_state: 'on_trip', vehicle_version: 1, route_id: 'lansdowne-centretown-v1', route_version: 1, route_segment: 0, segment_start: { latitude: 45.4, longitude: -75.6 }, segment_end: { latitude: 45.5, longitude: -75.7 }, segment_started_at: '2026-10-03T12:00:00Z', segment_ends_at: '2026-10-03T12:00:08Z', ...overrides });

describe('bounded simulation controls', () => {
  it('builds only the pinned route profile and enforces contract limits', () => {
    const manifest = scenarioManifest(settings, 'scenario_key_01');
    expect(manifest).toMatchObject({ scenario_id: 'route20synthetic', fleet_profile: 'route20synthetic', request_count: 2, execution_event_count: 6, route_id: 'lansdowne-centretown-v1', seed: 42 });
    expect(() => scenarioManifest({ ...settings, requestCount: SIMULATION_LIMITS.requests + 1 }, 'scenario_key_01')).toThrow(/1 and 20/);
    expect(() => scenarioManifest({ ...settings, concurrency: 5 }, 'scenario_key_01')).toThrow(/1 and 4/);
  });

  it('interpolates only fresh server-anchored route segments and clamps to endpoints', () => {
    expect(interpolatePosition(sample(), Date.parse('2026-10-03T12:00:04Z'))).toEqual({ latitude: 45.45, longitude: -75.65 });
    expect(interpolatePosition(sample(), Date.parse('2026-10-03T12:00:20Z'))).toEqual({ latitude: 45.5, longitude: -75.7 });
    expect(interpolatePosition(sample({ freshness: 'stale' }), Date.parse('2026-10-03T12:00:04Z'))).toEqual({ latitude: 45.4, longitude: -75.6 });
    expect(interpolatePosition(sample({ as_of: '2026-10-03T12:00:31Z' }), Date.parse('2026-10-03T12:00:31Z'))).toEqual({ latitude: 45.4, longitude: -75.6 });
  });

  it('limits concurrent position reads and encodes vehicle identifiers', async () => {
    let active = 0, peak = 0;
    await mapLimit([1, 2, 3, 4, 5, 6], 4, async () => { active += 1; peak = Math.max(peak, active); await new Promise(resolve => setTimeout(resolve, 1)); active -= 1; });
    expect(peak).toBeLessThanOrEqual(4);
    let requested = '';
    const fetcher: typeof fetch = vi.fn(async input => { requested = String(input); return new Response(JSON.stringify(sample()), { status: 200 }); });
    await getPosition('vehicle/1', fetcher);
    expect(requested).toBe('/fleet-api/v2/fleet/vehicles/vehicle%2F1/position');
  });

  it('waits for every bounded read to settle before surfacing a poll failure', async () => {
    let active = 0;
    await expect(mapLimit([1, 2, 3, 4, 5], 3, async value => {
      active += 1;
      await new Promise(resolve => setTimeout(resolve, value === 1 ? 1 : 8));
      active -= 1;
      if (value === 1) throw new Error('position unavailable');
      return value;
    })).rejects.toThrow('position unavailable');
    expect(active).toBe(0);
  });

  it('sends the required drain flag when stopping a run', async () => {
    let method = '', body = '';
    const fetcher: typeof fetch = vi.fn(async (_input, init) => {
      method = init?.method ?? '';
      body = String(init?.body ?? '');
      return new Response(JSON.stringify({ run_id: 'run-test-001', state: 'stopping', manifest: {}, created_at: '', deadline_at: '', issued_events: 0, completed_events: 0, version: 2 }), { status: 200 });
    });
    await stopRun('run-test-001', fetcher);
    expect(method).toBe('DELETE');
    expect(JSON.parse(body)).toEqual({ drain: true });
  });
});
