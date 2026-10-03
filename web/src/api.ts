import type { Ride, RideRequest, Vehicle } from './domain';

const FLEET_BASE = '/fleet-api';
const RIDE_BASE = '/ride-api';
const REQUEST_TIMEOUT_MS = 5_000;
const MAX_PAGES = 100;

type Page<T> = { items: T[]; next_cursor: string | null; as_of: string };
export type FleetResult = { items: Vehicle[]; asOf: string };
export type RideResult = { items: Ride[]; asOf: string };
export class ApiError extends Error {
  constructor(message: string, readonly status: number) { super(message); this.name = 'ApiError'; }
}

async function readJson<T>(response: Response): Promise<T> {
  if (!response.ok) {
    let detail = `Request failed (${response.status})`;
    try {
      const body = await response.json() as { code?: string; message?: string };
      detail = body.message ? `${body.code ?? 'error'}: ${body.message}` : detail;
    } catch { /* keep status-only message for non-JSON errors */ }
    throw new ApiError(detail, response.status);
  }
  return response.json() as Promise<T>;
}

async function readPages<T>(base: string, signal: AbortSignal, fetcher: typeof fetch): Promise<{ items: T[]; asOf: string }> {
  const all: T[] = [];
  const seenCursors = new Set<string>();
  let cursor: string | undefined;
  let asOf = '';
  for (let pageNumber = 0; pageNumber < MAX_PAGES; pageNumber += 1) {
    const query = new URLSearchParams({ limit: '100' });
    if (cursor) query.set('cursor', cursor);
    const path = base === FLEET_BASE ? 'fleet' : 'rides';
    const pageSignal = AbortSignal.any([signal, AbortSignal.timeout(REQUEST_TIMEOUT_MS)]);
    const page = await readJson<Page<T>>(await fetcher(`${base}/v1/${path}?${query}`, { signal: pageSignal }));
    if (!asOf) asOf = page.as_of;
    all.push(...page.items);
    if (!page.next_cursor) return { items: all, asOf };
    if (seenCursors.has(page.next_cursor)) throw new Error('List pagination returned a repeated cursor.');
    seenCursors.add(page.next_cursor);
    cursor = page.next_cursor;
  }
  throw new Error(`List exceeded the UI page limit of ${MAX_PAGES} pages.`);
}

export const getFleet = (signal: AbortSignal, fetcher: typeof fetch = fetch): Promise<FleetResult> => readPages<Vehicle>(FLEET_BASE, signal, fetcher);
export const getRides = (signal: AbortSignal, fetcher: typeof fetch = fetch): Promise<RideResult> => readPages<Ride>(RIDE_BASE, signal, fetcher);

export async function submitRide(body: RideRequest, key: string, fetcher: typeof fetch = fetch): Promise<Ride> {
  if (!/^[A-Za-z0-9_-]{8,128}$/.test(key)) throw new Error('Idempotency key must be 8–128 letters, numbers, underscores or hyphens.');
  const signal = AbortSignal.timeout(8_000);
  const response = await fetcher(`${RIDE_BASE}/v1/rides`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
    body: JSON.stringify(body),
    signal
  });
  return readJson<Ride>(response);
}
