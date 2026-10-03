import { expect, test } from '@playwright/test';

test('fixture operator flow labels synthetic data and enforces idempotent ride submissions', async ({ page }) => {
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.goto('/?fixture=1');
  await expect(page.getByText('Fixture mode · synthetic contract examples')).toBeVisible();
  await expect(page.getByRole('row', { name: /vehicle-006/ })).toContainText('Unknown');
  const key = 'smoke_ride_01';
  await page.getByLabel('Idempotency key').fill(key);
  await page.getByRole('button', { name: 'Submit ride' }).click();
  await expect(page.getByText('Ride accepted: ride-ui-001')).toBeVisible();
  await expect(page.locator('.ride-item')).toHaveCount(1);
  await expect(page.locator('.ride-item').first()).toContainText('ride-ui-001');
  await page.screenshot({ path: 'artifacts/operator-fixture.png', fullPage: true });

  await page.getByLabel('Idempotency key').fill(key);
  await page.getByLabel('Passengers').fill('3');
  await page.getByRole('button', { name: 'Submit ride' }).click();
  await expect(page.getByText(/idempotency_conflict/)).toBeVisible();
  await expect(page.locator('.ride-item')).toHaveCount(1);
  await expect(page.locator('.ride-item').first()).toContainText('ride-ui-001');
  await expect(page.locator('.ride-item').first()).toContainText('Completed', { timeout: 12_000 });
});

test('Ottawa fleet map shows approximate zone counts and recovers after a tile outage', async ({ page }) => {
  let tilesBlocked = true;
  await page.route('https://tile.openstreetmap.org/**', route => tilesBlocked
    ? route.abort()
    : route.fulfill({ status: 200, contentType: 'image/svg+xml', body: '<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256"><rect width="256" height="256" fill="#d8e3d9"/></svg>' }));
  await page.goto('/?fixture=1');

  const map = page.getByRole('region', { name: 'Ottawa fleet map' });
  await expect(map).toBeVisible();
  await expect(map).toHaveAttribute('data-map-center', '45.4215,-75.6972');
  await expect(page.getByText('© OpenStreetMap contributors')).toBeVisible();
  await expect(page.getByText(/Approximate zone locations/)).toBeVisible();
  await expect(page.getByText(/v1 API does not provide vehicle GPS positions/i)).toBeVisible();
  await expect(page.locator('#zone-locations li').filter({ hasText: 'Lansdowne' })).toContainText('2 vehicles · 2 available · 0 unknown');
  await expect(page.locator('#zone-locations li').filter({ hasText: 'ByWard Market' })).toContainText('1 vehicle · 0 available · 1 unknown');
  await expect(page.getByText(/map tiles are unavailable/i)).toBeVisible({ timeout: 10_000 });
  await expect(page.getByRole('button', { name: 'Submit ride' })).toBeEnabled();

  tilesBlocked = false;
  await page.getByRole('button', { name: 'Zoom in' }).click();
  await expect(page.getByText('Map tiles loaded.')).toBeVisible({ timeout: 10_000 });
});

test('zone summary marks retained fleet observations as last known after a poll failure', async ({ page }) => {
  let fleetCalls = 0;
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.route('**/fleet-api/**', route => {
    fleetCalls += 1;
    if (fleetCalls === 1) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          items: [
            { vehicle_id: 'map-vehicle-available', zone: 'lansdowne', seats: 4, availability: 'available', observed_at: '2026-10-03T12:00:00Z' },
            { vehicle_id: 'map-vehicle-stale', zone: 'lansdowne', seats: 4, availability: 'available', observed_at: '2026-10-03T11:59:29Z' }
          ],
          next_cursor: null,
          as_of: '2026-10-03T12:00:00Z'
        })
      });
    }
    return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ code: 'temporarily_unavailable', message: 'simulated outage' }) });
  });
  await page.route('**/ride-api/**', route => route.abort());
  await page.goto('/');

  const lansdowne = page.locator('#zone-locations li').filter({ hasText: 'Lansdowne' });
  await expect(lansdowne).toContainText('2 vehicles · 1 available · 1 unknown');
  await expect(lansdowne).toContainText(/Last known.*as of Oct 3, 2026, 12:00:00 PM UTC/);
  await expect.poll(() => fleetCalls).toBeGreaterThan(1);
  await expect(page.getByRole('img', { name: /Lansdowne: Last known.*as of Oct 3, 2026, 12:00:00 PM UTC/ })).toBeVisible();
  await page.getByRole('img', { name: /Lansdowne: Last known/ }).click();
  await expect(page.locator('.leaflet-popup-content')).toContainText('Last known');
  await expect(page.locator('.leaflet-popup-content')).toContainText('as of Oct 3, 2026, 12:00:00 PM UTC');
});

test('unavailable fleet is not summarized as zero available vehicles', async ({ page }) => {
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.route('**/fleet-api/**', route => route.abort());
  await page.route('**/ride-api/**', route => route.abort());
  await page.goto('/');

  const byward = page.locator('#zone-locations li').filter({ hasText: 'ByWard Market' });
  await expect(byward).toContainText('Fleet data unavailable');
  await expect(byward).not.toContainText('0 available');
});

