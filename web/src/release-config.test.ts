import { describe, expect, it, vi } from 'vitest';
import { DEFAULT_APP_BACKGROUND_COLOR, loadAppBackgroundColor, parseAppBackgroundColor } from './release-config';

describe('runtime background configuration', () => {
  it('accepts only the two supported colors', () => {
    expect(parseAppBackgroundColor('blue')).toBe('blue');
    expect(parseAppBackgroundColor('green')).toBe('green');
    expect(parseAppBackgroundColor('blue; color:red')).toBe(DEFAULT_APP_BACKGROUND_COLOR);
    expect(parseAppBackgroundColor(undefined)).toBe(DEFAULT_APP_BACKGROUND_COLOR);
  });

  it('loads the bounded runtime color and falls back when unavailable', async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ backgroundColor: 'blue' }), { status: 200 }));
    await expect(loadAppBackgroundColor(fetcher)).resolves.toBe('blue');
    expect(fetcher).toHaveBeenCalledWith('/runtime-config.json', expect.objectContaining({ cache: 'no-store' }));
    await expect(loadAppBackgroundColor(vi.fn(async () => { throw new Error('offline'); }))).resolves.toBe('green');
  });

  it('aborts a runtime config request at the configured bound', async () => {
    const fetcher = vi.fn((_url: string | URL | Request, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
    }));
    await expect(loadAppBackgroundColor(fetcher, 5)).resolves.toBe('green');
    expect(fetcher.mock.calls[0][1]?.signal?.aborted).toBe(true);
  });
});
