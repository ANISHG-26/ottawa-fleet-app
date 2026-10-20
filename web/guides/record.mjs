// Opt-in, bounded recordings against an already-running synthetic simulation stack.
import { chromium, expect } from '@playwright/test';
import { mkdir, copyFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const args = process.argv.slice(2);
if (!args.includes('--record-live')) {
  console.error('This adds two synthetic runs/rides. Start a simulation stack, then use: npm run guide:record -- --record-live [--base-url http://127.0.0.1:18088]');
  process.exit(1);
}
const baseIndex = args.indexOf('--base-url');
const base = new URL(baseIndex < 0 ? 'http://127.0.0.1:18088' : args[baseIndex + 1]);
if (base.protocol !== 'http:' || !['localhost', '127.0.0.1', '[::1]'].includes(base.hostname)
    || base.username || base.password || base.pathname !== '/' || base.search || base.hash) {
  throw new Error('Use a plain loopback HTTP origin for an existing synthetic stack.');
}
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const output = path.join(root, 'videos');
const scratch = path.join(root, 'web/artifacts/guide-recordings', new Date().toISOString().replace(/[:.]/g, '-'));
await mkdir(scratch, { recursive: true });
const browser = await chromium.launch({ headless: true, channel: process.env.PLAYWRIGHT_CHANNEL });
const captures = [];
const metadata = { recordedAt: new Date().toISOString(), environment: 'Live synthetic simulation stack over loopback', sourceCommit: null, scenarios: [] };
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));

async function caption(page, text, milliseconds = 1800) {
  await page.evaluate(text => {
    let banner = document.querySelector('[data-guide-caption]');
    if (!banner) {
      banner = document.createElement('aside');
      banner.setAttribute('data-guide-caption', '');
      banner.setAttribute('aria-hidden', 'true');
      Object.assign(banner.style, { position: 'fixed', bottom: '18px', left: '5%', width: '90%', zIndex: '100000', boxSizing: 'border-box', padding: '16px 22px', borderRadius: '12px', border: '1px solid #b9ef72', background: '#101b16f5', color: '#fff', font: '600 21px/1.4 system-ui', boxShadow: '0 4px 24px #0009', pointerEvents: 'none' });
      document.body.appendChild(banner);
    }
    banner.textContent = text;
  }, text);
  await pause(milliseconds); // Reading time is intentional in a human-facing recording.
}

async function json(request, endpoint) {
  const response = await request.get(endpoint);
  expect(response.ok(), endpoint).toBeTruthy();
  return response.json();
}

async function configure(page) {
  await page.locator('#simulation-heading').scrollIntoViewIfNeeded();
  await page.getByLabel('Requests', { exact: true }).fill('1');
  await page.getByLabel('Events / second', { exact: true }).fill('1');
  await page.getByLabel('Duration (seconds)', { exact: true }).fill('30');
  await page.getByLabel('Concurrency', { exact: true }).fill('1');
  await page.locator('#sim-opt-in').check();
}

async function start(page) {
  const response = page.waitForResponse(r => r.request().method() === 'POST'
    && new URL(r.url()).pathname === '/simulation-api/v2/simulation/runs');
  await page.getByRole('button', { name: 'Start scenario', exact: true }).click();
  const accepted = await response;
  expect(accepted.status()).toBe(200);
  return accepted.json();
}

async function verifyTerminal(context, runId, state) {
  let run;
  await expect.poll(async () => {
    run = await json(context.request, `/simulation-api/v2/simulation/runs/${runId}`);
    return run.state;
  }, { timeout: 45000 }).toBe(state);
  expect(run.completed_events).toBe(2);
  expect(run.incomplete_trips + run.incomplete_requests + run.incomplete_events).toBe(0);
  const events = await json(context.request, `/simulation-api/v2/simulation/runs/${runId}/events?limit=20`);
  expect(events.items).toHaveLength(2);
  expect(events.items.every(event => event.result === 'applied')).toBeTruthy();
  const trip = await json(context.request, `/ride-api/v2/rides/${events.items[0].ride_id}/trip`);
  expect(trip.trip_state).toBe('completed');
  return { runId, state, completedEvents: run.completed_events, incompleteWork: 0, vehicleId: trip.vehicle_id };
}

async function record(name, title, action) {
  const context = await browser.newContext({ baseURL: base.origin, viewport: { width: 1280, height: 960 }, recordVideo: { dir: scratch, size: { width: 1280, height: 960 } } });
  const page = await context.newPage();
  let ownedRun;
  const own = id => { ownedRun = id; };
  try {
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Service overview', exact: true })).toBeVisible();
    const build = await json(context.request, '/buildinfo');
    if (metadata.sourceCommit && metadata.sourceCommit !== build.source_commit) throw new Error('Release changed during recording.');
    metadata.sourceCommit = build.source_commit;
    const result = await action(page, context, own);
    await page.screenshot({ path: path.join(scratch, name + '.png') });
    captures.push({ name, video: page.video(), poster: path.join(scratch, name + '.png') });
    metadata.scenarios.push({ name, title, ...result, status: 'passed' });
  } finally {
    try {
      if (ownedRun) {
        const state = await json(context.request, `/simulation-api/v2/simulation/runs/${ownedRun}`);
        if (['scheduled', 'running', 'stopping'].includes(state.state)) {
          const stop = await context.request.delete(`/simulation-api/v2/simulation/runs/${ownedRun}`, { data: { drain: true } });
          expect(stop.ok()).toBeTruthy();
          await expect.poll(async () => (await json(context.request, `/simulation-api/v2/simulation/runs/${ownedRun}`)).state, { timeout: 25000 }).toMatch(/^(completed|stopped)$/);
        }
      }
    } finally { await context.close(); } // Playwright finalizes the WebM here.
  }
}

try {
  // Refuse to overlap a human's active simulation or record the wrong fixture.
  const check = await browser.newContext({ baseURL: base.origin });
  try {
    const runs = await json(check.request, '/simulation-api/v2/simulation/runs?limit=100');
    if (runs.next_cursor) throw new Error('Run history exceeds this bounded preflight; inspect it before recording.');
    expect(runs.items.filter(r => ['scheduled', 'running', 'stopping'].includes(r.state))).toHaveLength(0);
    const fleet = await json(check.request, '/fleet-api/v1/fleet?limit=100');
    expect(fleet.items).toHaveLength(20);
    expect(fleet.items.filter(v => v.zone === 'lansdowne' && v.availability === 'available' && !v.reserved).length).toBeGreaterThanOrEqual(2);
  } finally { await check.close(); }

  await record('01-fleet-and-freshness', 'Read the fleet and spot stale data', async page => {
    await expect(page.locator('#fleet-state')).toContainText('Connected');
    await caption(page, '1 / Fleet overview â€” 20 synthetic vehicles across Ottawa.');
    await page.getByRole('heading', { name: 'Fleet by zone', exact: true }).scrollIntoViewIfNeeded();
    await caption(page, 'The map groups vehicles by neighborhood. These are synthetic locations.', 2500);
    const stale = page.getByRole('row').filter({ hasText: 'vehicle-006' });
    await stale.scrollIntoViewIfNeeded();
    await expect(stale).toContainText('Unknown');
    await expect(stale).toContainText('freshness window');
    await caption(page, 'Old observations become Unknown. We do not present stale data as live.', 3000);
    return { staleVehicle: 'vehicle-006', writes: 0 };
  });

  await record('02-trip-and-refresh', 'Follow a trip and refresh safely', async (page, context, own) => {
    await configure(page);
    await caption(page, '2 / Start one trip â€” one request, concurrency one, bounded to 30 seconds.');
    const run = await start(page); own(run.run_id);
    await expect(page.locator('#sim-summary')).toContainText(run.run_id, { timeout: 12000 });
    await caption(page, 'The controller runs the trip on the server. Refreshing should keep this run.', 1000);
    await page.reload();
    await expect(page.locator('#sim-summary')).toContainText(run.run_id, { timeout: 12000 });
    await page.locator('#simulation-heading').scrollIntoViewIfNeeded();
    await caption(page, 'After refresh: the same run ID returns. No second Start was submitted.', 2200);
    const result = await verifyTerminal(context, run.run_id, 'completed');
    await expect(page.locator('#sim-summary')).toContainText('completed', { timeout: 12000 });
    await page.locator('#sim-trip-list').scrollIntoViewIfNeeded();
    await expect(page.locator('#sim-trip-list')).toContainText('Trip: Completed', { timeout: 12000 });
    await caption(page, 'Assignment and passenger trip are separate. Here both are complete.', 3000);
    return { ...result, refreshRestoredSameRun: true };
  });

  await record('03-stop-and-drain', 'Stop demand while finishing accepted work', async (page, context, own) => {
    await configure(page);
    await caption(page, '3 / Stop and drain â€” let an accepted trip finish safely.');
    const run = await start(page); own(run.run_id);
    await expect.poll(async () => {
      const events = await json(context.request, `/simulation-api/v2/simulation/runs/${run.run_id}/events?limit=20`);
      const started = events.items.find(event => event.kind === 'trip_start' && event.ride_id && event.result === 'applied');
      if (!started) return 'waiting';
      return (await json(context.request, `/ride-api/v2/rides/${started.ride_id}/trip`)).trip_state;
    }, { timeout: 12000 }).toBe('in_progress');
    await caption(page, 'A passenger trip is in progress. Stop prevents new work; accepted work drains.', 700);
    await page.getByRole('button', { name: 'Stop and drain', exact: true }).click();
    await caption(page, 'Stopping: the server reconciles the trip instead of abandoning it.', 1200);
    const result = await verifyTerminal(context, run.run_id, 'stopped');
    await expect(page.locator('#sim-summary')).toContainText('incomplete 0 trips / 0 requests / 0 events', { timeout: 12000 });
    await page.locator('#sim-summary').scrollIntoViewIfNeeded();
    await caption(page, 'Stopped â€” two applied events, completed trip, zero incomplete work.', 3000);
    return result;
  });
  await mkdir(output, { recursive: true });
  for (const capture of captures) {
    await capture.video.saveAs(path.join(output, capture.name + '.webm'));
    await copyFile(capture.poster, path.join(output, capture.name + '.png'));
  }
  await writeFile(path.join(output, 'recordings.json'), JSON.stringify(metadata, null, 2) + '\n');
  console.log(JSON.stringify(metadata, null, 2));
} finally { await browser.close(); }
