export const SIMULATION_LIMITS = { requests: 20, events: 60, rate: 4, durationSeconds: 60, concurrency: 4, vehicles: 20, positionReads: 4, freshnessMs: 30_000 } as const;
export const ROUTE_ID = 'lansdowne-centretown-v1';
export type Point = { latitude: number; longitude: number };
export type Route = { route_id: typeof ROUTE_ID; route_version: 1; start_zone: 'lansdowne'; end_zone: 'centretown'; duration_seconds: 8; points: Point[] };
export type Position = {
  vehicle_id: string; position: Point; observed_at: string; as_of: string; freshness: 'fresh' | 'stale' | 'future';
  operational_state: 'available' | 'reserved' | 'on_trip' | 'out_of_service'; vehicle_version: number;
  route_id?: typeof ROUTE_ID; route_version?: 1; route_segment?: number; segment_start?: Point; segment_end?: Point;
  segment_started_at?: string; segment_ends_at?: string;
};
export type Run = { run_id: string; state: string; manifest: Record<string, unknown>; created_at: string; deadline_at: string; issued_events: number; completed_events: number; version: number };
export type SimulationEvent = { event_id: string; sequence: number; kind: string; result: string; scheduled_at: string; issued_at?: string };
export type ScenarioSettings = { requestCount: number; rate: number; durationSeconds: number; concurrency: number; seed: number };

export function scenarioManifest(settings: ScenarioSettings, key: string) {
  const { requestCount, rate, durationSeconds, concurrency, seed } = settings;
  if (!Number.isInteger(requestCount) || requestCount < 1 || requestCount > SIMULATION_LIMITS.requests) throw new Error('Requests must be between 1 and 20.');
  if (!Number.isInteger(rate) || rate < 1 || rate > SIMULATION_LIMITS.rate) throw new Error('Event rate must be between 1 and 4 per second.');
  if (!Number.isInteger(durationSeconds) || durationSeconds < 1 || durationSeconds > SIMULATION_LIMITS.durationSeconds) throw new Error('Duration must be between 1 and 60 seconds.');
  if (!Number.isInteger(concurrency) || concurrency < 1 || concurrency > SIMULATION_LIMITS.concurrency) throw new Error('Concurrency must be between 1 and 4.');
  if (!Number.isInteger(seed) || seed < 0 || seed > 2_147_483_647) throw new Error('Seed must be between 0 and 2147483647.');
  if (!/^[A-Za-z0-9_-]{8,128}$/.test(key)) throw new Error('Idempotency key must be 8–128 letters, numbers, underscores or hyphens.');
  return { scenario_id: 'route20synthetic', fleet_profile: 'route20synthetic', idempotency_key: key,
    event_rate_per_second: rate, request_count: requestCount, execution_event_count: Math.min(SIMULATION_LIMITS.events, requestCount * 3),
    duration_seconds: durationSeconds, max_in_flight: concurrency, route_duration_seconds: 8,
    route_id: ROUTE_ID, start_zone: 'lansdowne', end_zone: 'centretown', seed } as const;
}

async function json<T>(path: string, init?: RequestInit, fetcher: typeof fetch = fetch): Promise<T> {
  const response = await fetcher(path, { ...init, signal: AbortSignal.any([init?.signal ?? new AbortController().signal, AbortSignal.timeout(5_000)]),
    headers: { ...(init?.headers ?? {}), ...(init?.body ? { 'Content-Type': 'application/json' } : {}) } });
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try { const body = await response.json() as { code?: string; message?: string }; message = `${body.code ?? 'error'}: ${body.message ?? message}`; } catch { /* status is enough */ }
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}
const SIM = '/simulation-api/v2/simulation';
export const getRoute = (fetcher: typeof fetch = fetch) => json<Route>(`${SIM}/routes/${ROUTE_ID}`, undefined, fetcher);
export const createRun = (manifest: ReturnType<typeof scenarioManifest>, fetcher: typeof fetch = fetch) => json<Run>(`${SIM}/runs`, { method: 'POST', body: JSON.stringify({ manifest }) }, fetcher);
export const getRun = (id: string, fetcher: typeof fetch = fetch) => json<Run>(`${SIM}/runs/${encodeURIComponent(id)}`, undefined, fetcher);
export const getRunEvents = (id: string, fetcher: typeof fetch = fetch) => json<{ items: SimulationEvent[]; next_cursor: string | null }>(`${SIM}/runs/${encodeURIComponent(id)}/events?limit=20`, undefined, fetcher);
export const stopRun = (id: string, fetcher: typeof fetch = fetch) => json<Run>(`${SIM}/runs/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ drain: true }) }, fetcher);
export type FleetVehicle = { vehicle_id: string };
export async function getVehicles(fetcher: typeof fetch = fetch): Promise<string[]> {
  const response = await json<{ items: FleetVehicle[] }>(`/fleet-api/v1/fleet?limit=20`, undefined, fetcher);
  return response.items.slice(0, SIMULATION_LIMITS.vehicles).map(item => item.vehicle_id);
}
export const getPosition = (id: string, fetcher: typeof fetch = fetch) => json<Position>(`/fleet-api/v2/fleet/vehicles/${encodeURIComponent(id)}/position`, undefined, fetcher);

export function interpolatePosition(position: Position, now = Date.now()): Point {
  const asOf = Date.parse(position.as_of);
  const observed = Date.parse(position.observed_at);
  if (position.freshness !== 'fresh' || !Number.isFinite(asOf) || !Number.isFinite(observed) || asOf - observed < 0 || asOf - observed > SIMULATION_LIMITS.freshnessMs || now - asOf > SIMULATION_LIMITS.freshnessMs || asOf - now > SIMULATION_LIMITS.freshnessMs) return position.position;
  if (position.operational_state !== 'on_trip' || !position.segment_start || !position.segment_end || !position.segment_started_at || !position.segment_ends_at) return position.position;
  const start = Date.parse(position.segment_started_at), end = Date.parse(position.segment_ends_at);
  if (!(end > start)) return position.position;
  const fraction = Math.max(0, Math.min(1, (now - start) / (end - start)));
  return { latitude: position.segment_start.latitude + (position.segment_end.latitude - position.segment_start.latitude) * fraction,
    longitude: position.segment_start.longitude + (position.segment_end.longitude - position.segment_start.longitude) * fraction };
}

export async function mapLimit<T, R>(items: T[], limit: number, work: (item: T) => Promise<R>): Promise<R[]> {
  const result: R[] = new Array(items.length); const failures: unknown[] = []; let next = 0;
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const index = next++;
      try { result[index] = await work(items[index]); } catch (error) { failures.push(error); }
    }
  }));
  if (failures.length) throw failures[0];
  return result;
}
