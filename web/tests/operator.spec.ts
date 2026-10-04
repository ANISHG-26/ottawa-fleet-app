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
  await page.setViewportSize({ width: 1440, height: 1000 });
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

  async function expectGlebeAndLansdowneLabelsClearOfEachOtherAndInsideMap() {
    await expect.poll(async () => {
      const mapBox = await map.boundingBox();
      const glebeBox = await page.locator('.map-zone-icon-west').boundingBox();
      const lansdowneBox = await page.locator('.map-zone-icon-east').boundingBox();
      return Boolean(mapBox && glebeBox && lansdowneBox
        && glebeBox.x + glebeBox.width < lansdowneBox.x
        && glebeBox.x >= mapBox.x
        && lansdowneBox.x + lansdowneBox.width <= mapBox.x + mapBox.width);
    }).toBe(true);
  }

  await expectGlebeAndLansdowneLabelsClearOfEachOtherAndInsideMap();
  await page.setViewportSize({ width: 390, height: 844 });
  await expectGlebeAndLansdowneLabelsClearOfEachOtherAndInsideMap();

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

test('route simulation writes only after opt-in and bounds position polling', async ({ page }, testInfo) => {
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  let postCount = 0, stopCount = 0, activePositionReads = 0, peakPositionReads = 0;
  const now = new Date().toISOString();
  const routeFixture = { route_id: 'lansdowne-centretown-v1', route_version: 1, start_zone: 'lansdowne', end_zone: 'centretown', duration_seconds: 8, points: Array.from({ length: 21 }, (_, i) => ({ latitude: 45.3995 + i * 0.001, longitude: -75.6823 - i * 0.0005 })) };
  const runFixture = { run_id: 'run-ui-001', state: 'running', manifest: {}, created_at: now, deadline_at: now, issued_events: 1, completed_events: 0, version: 1 };
  await page.route('**/simulation-api/v2/simulation/routes/**', route => route.fulfill({ json: routeFixture }));
  await page.route('**/simulation-api/v2/simulation/runs/run-ui-001/events**', route => route.fulfill({ json: { items: [{ event_id: 'event-ui-001', sequence: 1, kind: 'trip_start', result: 'applied', scheduled_at: now }], next_cursor: null } }));
  await page.route('**/simulation-api/v2/simulation/runs', route => {
    postCount += 1; return route.fulfill({ json: runFixture });
  });
  await page.route('**/simulation-api/v2/simulation/runs/run-ui-001', route => {
    if (route.request().method() === 'DELETE') { stopCount += 1; return route.fulfill({ json: { ...runFixture, state: 'stopping' } }); }
    return route.fulfill({ json: runFixture });
  });
  await page.route('**/fleet-api/v1/fleet?limit=20', route => route.fulfill({ json: { items: Array.from({ length: 6 }, (_, i) => ({ vehicle_id: `vehicle-00${i + 1}` })), next_cursor: null, as_of: now } }));
  await page.route('**/fleet-api/v2/fleet/vehicles/*/position', async route => {
    activePositionReads += 1; peakPositionReads = Math.max(peakPositionReads, activePositionReads);
    await new Promise(resolve => setTimeout(resolve, 15));
    activePositionReads -= 1;
    await route.fulfill({ json: { vehicle_id: 'vehicle-001', position: { latitude: 45.3995, longitude: -75.6823 }, observed_at: now, as_of: now, freshness: 'fresh', operational_state: 'available', vehicle_version: 1 } });
  });
  await page.goto('/');
  const start = page.getByRole('button', { name: 'Start scenario' });
  await expect(start).toBeDisabled();
  expect(postCount).toBe(0);
  await page.getByLabel(/Start an opt-in synthetic run/).check();
  await expect(start).toBeEnabled();
  await start.click();
  await expect(page.getByText(/Run run-ui-001 · running/)).toBeVisible();
  await expect(page.getByText(/#1 trip_start · applied/)).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('simulation-running.png'), fullPage: true });
  expect(postCount).toBe(1);
  expect(peakPositionReads).toBeLessThanOrEqual(4);
  await page.getByRole('button', { name: 'Stop and drain' }).click();
  await expect.poll(() => stopCount).toBe(1);
});

