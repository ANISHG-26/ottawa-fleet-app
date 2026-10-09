import { expect, test } from '@playwright/test';

test('runtime blue background config changes the app panels', async ({ page }) => {
  await page.route('**/runtime-config.json', route => route.fulfill({ json: { backgroundColor: 'blue' }, headers: { 'cache-control': 'no-store' } }));
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.goto('/?fixture=1');
  await expect(page.locator('html')).toHaveAttribute('data-app-background', 'blue');
  await expect.poll(() => page.locator('.panel').first().evaluate(element => getComputedStyle(element).backgroundColor)).toBe('rgb(20, 38, 48)');
});

test('runtime background config failure falls back to green', async ({ page }) => {
  await page.route('**/runtime-config.json', route => route.abort());
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.goto('/?fixture=1');
  await expect(page.locator('html')).toHaveAttribute('data-app-background', 'green');
  await expect.poll(() => page.locator('.panel').first().evaluate(element => getComputedStyle(element).backgroundColor)).toBe('rgba(0, 0, 0, 0)');
});

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
  await page.route('**/fleet-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  await page.route('**/ride-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  let postCount = 0, stopCount = 0, runReadCount = 0, activePositionReads = 0, peakPositionReads = 0;
  const now = new Date().toISOString();
  const routeFixture = { route_id: 'lansdowne-centretown-v1', route_version: 1, start_zone: 'lansdowne', end_zone: 'centretown', duration_seconds: 8, points: Array.from({ length: 21 }, (_, i) => ({ latitude: 45.3995 + i * 0.001, longitude: -75.6823 - i * 0.0005 })) };
  const runFixture = { run_id: 'run-ui-001', state: 'running', manifest: {}, created_at: now, deadline_at: now, issued_events: 1, completed_events: 0, version: 1 };
  let currentRun = runFixture;
  await page.route('**/simulation-api/v2/simulation/routes/**', route => route.fulfill({ json: routeFixture }));
  await page.route('**/simulation-api/v2/simulation/runs/run-ui-001/events**', route => route.fulfill({ json: { items: [{ event_id: 'event-ui-001', sequence: 1, kind: 'trip_start', result: 'applied', scheduled_at: now, ride_id: 'ride-ui-001', trip_id: 'trip-ui-001', vehicle_id: 'vehicle-001' }], next_cursor: null } }));
  await page.route('**/ride-api/v2/rides/ride-ui-001/trip', route => route.fulfill({ json: { ride_id: 'ride-ui-001', assignment_state: 'completed', trip_state: 'in_progress', trip_id: 'trip-ui-001', vehicle_id: 'vehicle-001', start_zone: 'lansdowne', destination_zone: 'centretown', updated_at: now, version: 2 } }));
  await page.route('**/simulation-api/v2/simulation/runs', route => {
    postCount += 1; return route.fulfill({ json: runFixture });
  });
  await page.route('**/simulation-api/v2/simulation/runs/run-ui-001', route => {
    if (route.request().method() === 'DELETE') { stopCount += 1; currentRun = { ...runFixture, state: 'stopped', terminal_reason: 'requested_stop', incomplete_trips: 1, incomplete_requests: 1, incomplete_events: 2, completed_at: now }; return route.fulfill({ json: currentRun }); }
    runReadCount += 1;
    return route.fulfill({ json: currentRun });
  });
  await page.route('**/fleet-api/v1/fleet?limit=20', route => route.fulfill({ json: { items: Array.from({ length: 6 }, (_, i) => ({ vehicle_id: `vehicle-00${i + 1}` })), next_cursor: null, as_of: now } }));
  await page.route('**/fleet-api/v2/fleet/vehicles/*/position', async route => {
    activePositionReads += 1; peakPositionReads = Math.max(peakPositionReads, activePositionReads);
    await new Promise(resolve => setTimeout(resolve, 15));
    activePositionReads -= 1;
    const vehicleId = route.request().url().split('/').at(-2)!;
    await route.fulfill({ json: { vehicle_id: vehicleId, position: { latitude: 45.3995, longitude: -75.6823 }, observed_at: now, as_of: now, freshness: 'fresh', operational_state: 'available', vehicle_version: 1 } });
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
  await expect(page.getByText(/Assignment: Completed/)).toBeVisible();
  await expect(page.getByText(/Trip: In Progress/)).toBeVisible();
  await expect(page.locator('#sim-requests')).toBeDisabled();
  await page.screenshot({ path: testInfo.outputPath('simulation-running.png'), fullPage: true });
  expect(postCount).toBe(1);
  expect(peakPositionReads).toBeLessThanOrEqual(4);
  await page.getByLabel('Vehicle on map').selectOption('vehicle-002');
  await expect(page.locator('.leaflet-overlay-pane .simulation-vehicle-marker')).toHaveCount(1);
  await expect(page.locator('#sim-vehicle-detail')).toContainText('vehicle-002:');
  await page.getByLabel('Vehicle on map').selectOption('vehicle-001');
  await expect(page.locator('.leaflet-overlay-pane .simulation-vehicle-marker')).toHaveCount(1);
  await expect(page.locator('#sim-vehicle-detail')).toContainText('vehicle-001:');
  await page.reload();
  await expect(page.getByText(/Run run-ui-001 · running/)).toBeVisible();
  await expect(page.getByText(/#1 trip_start · applied/)).toBeVisible();
  await expect(page.getByLabel('Vehicle on map')).toHaveValue('vehicle-001');
  expect(postCount).toBe(1);
  expect(runReadCount).toBeGreaterThan(0);
  await page.getByRole('button', { name: 'Stop and drain' }).click();
  await expect.poll(() => stopCount).toBe(1);
  await expect(page.locator('#sim-summary')).toContainText('incomplete 1 trips / 1 requests / 2 events');
  await expect(page.locator('#sim-summary')).toContainText('Requested Stop');
  await page.reload();
  await expect(page.locator('#sim-summary')).toContainText('incomplete 1 trips / 1 requests / 2 events');
  await expect(page.getByText(/Assignment: Completed/)).toBeVisible();
  await expect(page.locator('#sim-requests')).toBeEnabled();
  await page.locator('.sim-trip-card').click();
  await expect(page.getByLabel('Vehicle on map')).toHaveValue('vehicle-001');
});

test('completed run restores trip, selected vehicle and textual last-known age', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.route('**/fleet-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  await page.route('**/ride-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  const now = new Date().toISOString();
  const routeFixture = { route_id: 'lansdowne-centretown-v1', route_version: 1, start_zone: 'lansdowne', end_zone: 'centretown', duration_seconds: 8, points: Array.from({ length: 21 }, (_, i) => ({ latitude: 45.3995 + i * 0.001, longitude: -75.6823 - i * 0.0005 })) };
  await page.addInitScript(() => localStorage.setItem('ottawa-fleet.simulation-run-id', 'run-terminal'));
  await page.route('**/simulation-api/v2/simulation/runs/run-terminal', route => route.fulfill({ json: { run_id: 'run-terminal', state: 'completed', manifest: { request_count: 1, event_rate_per_second: 1, duration_seconds: 30, max_in_flight: 1 }, created_at: now, deadline_at: now, completed_at: now, terminal_reason: 'all_events_complete', incomplete_trips: 0, incomplete_requests: 0, incomplete_events: 0, issued_events: 2, completed_events: 2, version: 3 } }));
  await page.route('**/simulation-api/v2/simulation/runs/run-terminal/events**', route => route.fulfill({ json: { items: [{ event_id: 'event-terminal-start', sequence: 1, kind: 'trip_start', result: 'applied', scheduled_at: now, ride_id: 'ride-terminal', trip_id: 'trip-terminal', vehicle_id: 'vehicle-001' }, { event_id: 'event-terminal-complete', sequence: 2, kind: 'trip_complete', result: 'applied', scheduled_at: now, ride_id: 'ride-terminal', trip_id: 'trip-terminal', vehicle_id: 'vehicle-001' }], next_cursor: null } }));
  await page.route('**/simulation-api/v2/simulation/routes/**', route => route.fulfill({ json: routeFixture }));
  await page.route('**/ride-api/v2/rides/ride-terminal/trip', route => route.fulfill({ json: { ride_id: 'ride-terminal', assignment_state: 'completed', trip_state: 'completed', trip_id: 'trip-terminal', vehicle_id: 'vehicle-001', updated_at: now, version: 3 } }));
  await page.route('**/fleet-api/v1/fleet?limit=20', route => route.fulfill({ json: { items: [{ vehicle_id: 'vehicle-001' }, { vehicle_id: 'vehicle-002' }], next_cursor: null, as_of: now } }));
  let positionCalls = 0;
  await page.route('**/fleet-api/v2/fleet/vehicles/*/position', route => {
    positionCalls += 1;
    if (route.request().url().includes('vehicle-002')) return route.abort();
    return route.fulfill({ json: { vehicle_id: 'vehicle-001', position: { latitude: 45.3995, longitude: -75.6823 }, observed_at: '2026-10-03T12:00:00Z', as_of: '2026-10-03T12:00:00Z', freshness: 'fresh', operational_state: 'available', vehicle_version: 4 } });
  });
  await page.route('**/ride-api/v1/rides**', route => route.fulfill({ json: { items: [{ ride_id: 'ride-terminal', pickup_zone: 'lansdowne', dropoff_zone: 'centretown', passengers: 1, state: 'completed', vehicle_id: 'vehicle-001', created_at: now, updated_at: now }], next_cursor: null, as_of: now } }));
  await page.goto('/');
  await expect(page.locator('#sim-summary')).toContainText('completed');
  await expect(page.locator('#sim-summary')).toContainText('All Events Complete');
  await expect(page.locator('#sim-summary')).toContainText('1 planned requests');
  await expect(page.locator('#sim-summary')).toContainText('incomplete 0 trips / 0 requests / 0 events');
  await expect(page.getByText(/Trip: Completed/)).toBeVisible();
  await expect(page.locator('#sim-vehicle-detail')).toContainText('Last known');
  await expect.poll(() => positionCalls).toBeGreaterThan(0);
  await expect(page.getByRole('button', { name: /Select ride-terminal trip and vehicle/ })).toBeVisible();
  await page.getByRole('button', { name: /Select ride-terminal trip and vehicle/ }).click();
  await expect(page.getByLabel('Vehicle on map')).toHaveValue('vehicle-001');
  await expect(page.locator('.leaflet-overlay-pane .simulation-vehicle-marker')).toHaveCount(1);
  await page.reload();
  await expect(page.locator('#sim-summary')).toContainText('completed');
  await expect(page.getByText(/Trip: Completed/)).toBeVisible();
  await expect(page.getByLabel('Vehicle on map')).toHaveValue('vehicle-001');
  await expect(page.locator('.leaflet-overlay-pane .simulation-vehicle-marker')).toHaveCount(1);
});

test('uncertain run creation retries the exact pending manifest and idempotency key', async ({ page }) => {
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.route('**/fleet-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  await page.route('**/ride-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  const now = new Date().toISOString();
  const acceptedRun = { run_id: 'run-reconciled', state: 'running', manifest: {}, created_at: now, deadline_at: now, issued_events: 0, completed_events: 0, version: 1 };
  let postCount = 0;
  const submitted: unknown[] = [];
  await page.route('**/simulation-api/v2/simulation/routes/**', route => route.fulfill({ json: { route_id: 'lansdowne-centretown-v1', route_version: 1, start_zone: 'lansdowne', end_zone: 'centretown', duration_seconds: 8, points: [{ latitude: 45.3995, longitude: -75.6823 }, { latitude: 45.4148, longitude: -75.6984 }] } }));
  await page.route('**/simulation-api/v2/simulation/runs', async route => {
    submitted.push(route.request().postDataJSON());
    postCount += 1;
    if (postCount === 1) return route.abort();
    return route.fulfill({ json: acceptedRun });
  });
  await page.route('**/simulation-api/v2/simulation/runs/run-reconciled', route => route.fulfill({ json: acceptedRun }));
  await page.route('**/simulation-api/v2/simulation/runs/run-reconciled/events**', route => route.fulfill({ json: { items: [], next_cursor: null } }));
  await page.route('**/fleet-api/v1/fleet?limit=20', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: now } }));
  await page.goto('/');
  await page.getByLabel(/Start an opt-in synthetic run/).check();
  await page.getByRole('button', { name: 'Start scenario' }).click();
  await expect(page.getByText(/Start response is uncertain/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Retry same scenario start' })).toBeEnabled();
  await expect(page.locator('#sim-requests')).toBeDisabled();
  await page.getByRole('button', { name: 'Retry same scenario start' }).click();
  await expect(page.getByText(/Run run-reconciled · running/)).toBeVisible();
  expect(postCount).toBe(2);
  expect(submitted[1]).toEqual(submitted[0]);
  expect((submitted[1] as { manifest: { idempotency_key: string } }).manifest.idempotency_key).toBeTruthy();
});

test('accepting a different run replaces its trip and vehicle snapshot', async ({ page }) => {
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.route('**/fleet-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  await page.route('**/ride-api/**', route => route.fulfill({ json: { items: [], next_cursor: null, as_of: new Date().toISOString() } }));
  const now = new Date().toISOString();
  const oldRun = { run_id: 'run-old', state: 'completed', manifest: { request_count: 1 }, created_at: now, deadline_at: now, completed_at: now, terminal_reason: 'all_events_complete', incomplete_trips: 0, incomplete_requests: 0, incomplete_events: 0, issued_events: 2, completed_events: 2, version: 2 };
  const newRun = { run_id: 'run-new', state: 'running', manifest: { request_count: 1 }, created_at: now, deadline_at: now, issued_events: 0, completed_events: 0, version: 1 };
  await page.addInitScript(() => localStorage.setItem('ottawa-fleet.simulation-run-id', 'run-old'));
  await page.route('**/simulation-api/v2/simulation/routes/**', route => route.fulfill({ json: { route_id: 'lansdowne-centretown-v1', route_version: 1, start_zone: 'lansdowne', end_zone: 'centretown', duration_seconds: 8, points: [{ latitude: 45.3995, longitude: -75.6823 }, { latitude: 45.4148, longitude: -75.6984 }] } }));
  await page.route('**/simulation-api/v2/simulation/runs/run-old', route => route.fulfill({ json: oldRun }));
  await page.route('**/simulation-api/v2/simulation/runs/run-old/events**', route => route.fulfill({ json: { items: [{ event_id: 'old-start', sequence: 1, kind: 'trip_start', result: 'applied', scheduled_at: now, ride_id: 'ride-old', trip_id: 'trip-old', vehicle_id: 'vehicle-old' }], next_cursor: null } }));
  await page.route('**/ride-api/v2/rides/ride-old/trip', route => route.fulfill({ json: { ride_id: 'ride-old', assignment_state: 'completed', trip_state: 'completed', trip_id: 'trip-old', vehicle_id: 'vehicle-old', updated_at: now, version: 3 } }));
  await page.route('**/simulation-api/v2/simulation/runs', route => route.fulfill({ json: newRun }));
  await page.route('**/simulation-api/v2/simulation/runs/run-new', route => route.fulfill({ json: newRun }));
  await page.route('**/simulation-api/v2/simulation/runs/run-new/events**', route => route.fulfill({ json: { items: [], next_cursor: null } }));
  let inventoryCall = 0;
  await page.route('**/fleet-api/v1/fleet?limit=20', route => {
    inventoryCall += 1;
    const vehicle = inventoryCall === 1 ? 'vehicle-old' : 'vehicle-new';
    return route.fulfill({ json: { items: [{ vehicle_id: vehicle }], next_cursor: null, as_of: now } });
  });
  await page.route('**/fleet-api/v2/fleet/vehicles/*/position', route => {
    const vehicle = route.request().url().split('/').at(-2)!;
    return route.fulfill({ json: { vehicle_id: vehicle, position: { latitude: vehicle === 'vehicle-old' ? 45.3995 : 45.4148, longitude: -75.6823 }, observed_at: now, as_of: now, freshness: 'fresh', operational_state: 'available', vehicle_version: 1 } });
  });
  await page.goto('/');
  await expect(page.locator('.sim-trip-card[data-ride-id="ride-old"]')).toBeVisible();
  await page.getByLabel(/Start an opt-in synthetic run/).check();
  await page.getByRole('button', { name: 'Start scenario' }).click();
  await expect(page.getByText(/Run run-new · running/)).toBeVisible();
  await expect(page.locator('.sim-trip-card')).toHaveCount(0);
  await expect(page.getByLabel('Vehicle on map')).toContainText('vehicle-new');
  await expect(page.getByLabel('Vehicle on map')).not.toContainText('vehicle-old');
  await expect(page.getByText(/old-start/)).toHaveCount(0);
});

test('mobile layout contains long server ride and trip identifiers', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route('https://tile.openstreetmap.org/**', route => route.abort());
  await page.route('**/fleet-api/**', route => route.fulfill({ json: { items: [{ vehicle_id: 'vehicle_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0' }], next_cursor: null, as_of: new Date().toISOString() } }));
  await page.route('**/ride-api/v1/rides**', route => route.fulfill({ json: { items: [{ ride_id: 'ride_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0H2J4K6M8N0P2R4T6V8X0Z2A4C6E8G0', pickup_zone: 'lansdowne', dropoff_zone: 'centretown', passengers: 1, state: 'completed', vehicle_id: 'vehicle_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0', created_at: new Date().toISOString(), updated_at: new Date().toISOString() }], next_cursor: null, as_of: new Date().toISOString() } }));
  await page.route('**/ride-api/v2/rides/*/trip', route => route.fulfill({ json: { ride_id: 'ride_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0H2J4K6M8N0P2R4T6V8X0Z2A4C6E8G0', assignment_state: 'completed', trip_state: 'completed', trip_id: 'trip_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0H2J4K6M8N0P2R4T6V8X0Z2A4C6E8G0', vehicle_id: 'vehicle_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0', updated_at: new Date().toISOString(), version: 1 } }));
  await page.addInitScript(() => localStorage.setItem('ottawa-fleet.simulation-run-id', 'run-mobile'));
  const now = new Date().toISOString();
  await page.route('**/simulation-api/v2/simulation/runs/run-mobile', route => route.fulfill({ json: { run_id: 'run-mobile', state: 'completed', manifest: {}, created_at: now, deadline_at: now, completed_at: now, terminal_reason: 'all_events_complete', incomplete_trips: 0, incomplete_requests: 0, incomplete_events: 0, issued_events: 1, completed_events: 1, version: 1 } }));
  await page.route('**/simulation-api/v2/simulation/runs/run-mobile/events**', route => route.fulfill({ json: { items: [{ event_id: 'event-mobile', sequence: 1, kind: 'trip_start', result: 'applied', scheduled_at: now, ride_id: 'ride_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0H2J4K6M8N0P2R4T6V8X0Z2A4C6E8G0', trip_id: 'trip_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0H2J4K6M8N0P2R4T6V8X0Z2A4C6E8G0', vehicle_id: 'vehicle_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0' }], next_cursor: null } }));
  await page.route('**/simulation-api/v2/simulation/routes/**', route => route.fulfill({ json: { route_id: 'lansdowne-centretown-v1', route_version: 1, start_zone: 'lansdowne', end_zone: 'centretown', duration_seconds: 8, points: [{ latitude: 45.3995, longitude: -75.6823 }, { latitude: 45.4148, longitude: -75.6984 }] } }));
  await page.route('**/fleet-api/v2/fleet/vehicles/*/position', route => route.fulfill({ json: { vehicle_id: 'vehicle_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0', position: { latitude: 45.3995, longitude: -75.6823 }, observed_at: now, as_of: now, freshness: 'fresh', operational_state: 'available', vehicle_version: 1 } }));
  await page.goto('/');
  await expect(page.locator('.sim-trip-card')).toContainText('trip_01J8Y2R4M7T9V3X5Z6A8B0C2D4E6F8G0H2J4K6M8N0P2R4T6V8X0Z2A4C6E8G0');
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

