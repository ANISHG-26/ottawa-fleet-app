import { describe, expect, it } from 'vitest';
import { deriveAvailability, emptySnapshot, failedSnapshot, successfulSnapshot } from './domain';
import { getRides, submitRide } from './api';

describe('operator data states', () => {
  it('derives unknown when an observation is older than the contract freshness window', () => {
    expect(deriveAvailability('available', '2026-10-03T11:59:29Z', '2026-10-03T12:00:00Z')).toBe('unknown');
    expect(deriveAvailability('available', '2026-10-03T12:00:00Z', '2026-10-03T12:00:00Z')).toBe('available');
  });

  it('distinguishes initial loading, a successful empty response, and unavailable service', () => {
    expect(emptySnapshot().state).toBe('loading');
    expect(successfulSnapshot([], '2026-10-03T12:00:00Z').state).toBe('empty');
    expect(failedSnapshot(emptySnapshot()).state).toBe('unavailable');
  });

  it('keeps last successful values and timestamp after a poll failure', () => {
    const success = successfulSnapshot([{ ride_id: 'ride-001' }], '2026-10-03T12:00:00Z');
    const stale = failedSnapshot(success);
    expect(stale.state).toBe('stale');
    expect(stale.items).toEqual(success.items);
    expect(stale.lastSuccessAt).toBe(success.lastSuccessAt);
  });
});

describe('ride submission and list contracts', () => {
  it('sends the contract idempotency header and exact RideRequest body', async () => {
    let request: Request | undefined;
    const fetcher: typeof fetch = async (input, init) => {
      request = new Request(new URL(String(input), 'http://operator.local'), init);
      return new Response(JSON.stringify({ ride_id: 'ride-001', pickup_zone: 'lansdowne', dropoff_zone: 'centretown', passengers: 2, state: 'queued', created_at: '2026-10-03T12:00:00Z', updated_at: '2026-10-03T12:00:00Z' }), { status: 202, headers: { 'Content-Type': 'application/json', Location: '/v1/rides/ride-001' } });
    };
    await submitRide({ pickup_zone: 'lansdowne', dropoff_zone: 'centretown', passengers: 2 }, 'demo_ride_001', fetcher);
    expect(request?.url).toContain('/ride-api/v1/rides');
    expect(request?.headers.get('Idempotency-Key')).toBe('demo_ride_001');
    expect(await request?.json()).toEqual({ pickup_zone: 'lansdowne', dropoff_zone: 'centretown', passengers: 2 });
  });

  it('rejects cursor cycles instead of polling forever', async () => {
    const fetcher: typeof fetch = async () => new Response(JSON.stringify({ items: [], next_cursor: 'loop', as_of: '2026-10-03T12:00:00Z' }), { status: 200 });
    await expect(getRides(new AbortController().signal, fetcher)).rejects.toThrow(/repeated cursor/);
  });
});
