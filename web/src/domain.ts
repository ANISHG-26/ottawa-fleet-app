export type Availability = 'available' | 'reserved' | 'unavailable' | 'unknown';
export type SnapshotState = 'loading' | 'empty' | 'ready' | 'stale' | 'unavailable';
export type Snapshot<T> = { state: SnapshotState; items: T[]; asOf?: string; lastSuccessAt?: string; message?: string };

export function deriveAvailability(availability: Availability, observedAt: string, asOf: string): Availability {
  if (availability === 'unknown') return 'unknown';
  const ageMs = Date.parse(asOf) - Date.parse(observedAt);
  if (!Number.isFinite(ageMs) || ageMs < 0 || ageMs > 30_000) return 'unknown';
  return availability;
}

export function emptySnapshot<T>(): Snapshot<T> {
  return { state: 'loading', items: [] };
}

export function successfulSnapshot<T>(items: T[], asOf: string, receivedAt = new Date().toISOString()): Snapshot<T> {
  return { state: items.length === 0 ? 'empty' : 'ready', items, asOf, lastSuccessAt: receivedAt };
}

export function failedSnapshot<T>(previous: Snapshot<T>, message = 'API unavailable'): Snapshot<T> {
  if (previous.lastSuccessAt) return { ...previous, state: 'stale', message };
  return { state: 'unavailable', items: [], message };
}

export const ZONES = ['centretown', 'glebe', 'lansdowne', 'byward-market'] as const;
export type Zone = typeof ZONES[number];
export type RideRequest = { pickup_zone: Zone; dropoff_zone: Zone; passengers: number };
export type Vehicle = { vehicle_id: string; zone: Zone; seats: number; availability: Availability; observed_at: string };
export type Ride = RideRequest & { ride_id: string; state: 'queued' | 'processing' | 'completed' | 'failed'; created_at: string; updated_at: string; vehicle_id?: string; failure_code?: string };

export function makeIdempotencyKey(): string {
  const random = globalThis.crypto?.randomUUID?.().replaceAll('-', '') ?? `${Date.now()}${Math.random().toString(36).slice(2)}`;
  return `ride_${random}`;
}
