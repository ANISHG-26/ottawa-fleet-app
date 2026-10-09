export type AppBackgroundColor = 'green' | 'blue';

export const DEFAULT_APP_BACKGROUND_COLOR: AppBackgroundColor = 'green';
export const RUNTIME_CONFIG_URL = '/runtime-config.json';
export const RUNTIME_CONFIG_TIMEOUT_MS = 5_000;

export function parseAppBackgroundColor(value: unknown): AppBackgroundColor {
  return value === 'blue' || value === 'green' ? value : DEFAULT_APP_BACKGROUND_COLOR;
}

export async function loadAppBackgroundColor(
  fetcher: typeof fetch = fetch,
  timeoutMs = RUNTIME_CONFIG_TIMEOUT_MS
): Promise<AppBackgroundColor> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetcher(RUNTIME_CONFIG_URL, { cache: 'no-store', signal: controller.signal });
    if (!response.ok) return DEFAULT_APP_BACKGROUND_COLOR;
    const config: unknown = await response.json();
    if (typeof config !== 'object' || config === null || !('backgroundColor' in config)) {
      return DEFAULT_APP_BACKGROUND_COLOR;
    }
    return parseAppBackgroundColor(config.backgroundColor);
  } catch {
    return DEFAULT_APP_BACKGROUND_COLOR;
  } finally {
    clearTimeout(timeout);
  }
}
