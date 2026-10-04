import { ApiError, getFleet, getRides, submitRide } from './api';
import { deriveAvailability, emptySnapshot, failedSnapshot, makeIdempotencyKey, successfulSnapshot, ZONES, type Ride, type RideRequest, type Snapshot, type Vehicle } from './domain';
import { createRun, getPosition, getRoute, getRun, getRunEvents, getVehicles, interpolatePosition, mapLimit, scenarioManifest, stopRun, type Position, type Route, type Run, type SimulationEvent } from './simulation';
import L from 'leaflet';
import 'leaflet/dist/leaflet.css';
import './map.css';
import './style.css';

const fixtureMode = new URLSearchParams(location.search).get('fixture') === '1';
const app = document.querySelector<HTMLDivElement>('#app')!;
const fixtureClock = '2026-10-03T12:00:00Z';
const fixtureVehicles: Vehicle[] = Array.from({ length: 6 }, (_, i) => ({
  vehicle_id: `vehicle-00${i + 1}`,
  zone: (['lansdowne', 'lansdowne', 'centretown', 'centretown', 'glebe', 'byward-market'] as const)[i],
  seats: 4,
  availability: 'available',
  observed_at: i === 5 ? '2026-10-03T11:59:29Z' : fixtureClock
}));
const fixtureRides: Ride[] = [];
let fixtureRideNumber = 0;
let fleetSnapshot: Snapshot<Vehicle> = emptySnapshot();
let rideSnapshot: Snapshot<Ride> = emptySnapshot();
let controller: AbortController | undefined;
let timer: number | undefined;
let closed = false;
let lastPollAt = '';
let submitMessage = '';
let submitting = false;

const ZONE_POINTS = [
  { zone: 'centretown', name: 'Centretown', point: [45.4148, -75.6984] as L.LatLngExpression },
  { zone: 'glebe', name: 'The Glebe', point: [45.4027778, -75.6913889] as L.LatLngExpression },
  { zone: 'lansdowne', name: 'Lansdowne', point: [45.3995, -75.6823] as L.LatLngExpression },
  { zone: 'byward-market', name: 'ByWard Market', point: [45.4274, -75.6926] as L.LatLngExpression }
] as const;
const zoneMarkers = new Map<string, L.Marker>();
let fleetMap: L.Map | undefined;
const failedVisibleTileKeys = new Set<string>();
let simulationRun: Run | undefined;
let simulationRoute: Route | undefined;
let simulationVehicles: string[] = [];
let simulationPositions: Position[] = [];
let simulationEvents: SimulationEvent[] = [];
let simulationPollTimer: number | undefined;
let simulationFrame: number | undefined;
let simulationBusy = false;
const simulationMarkers = new Map<string, L.CircleMarker>();
let simulationRouteLine: L.Polyline | undefined;
let simulationMessage = 'Controller not checked yet.';
let selectedSimulationVehicle = 'all';

const escapeHtml = (value: string) => value.replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[char]!));
const formatTime = (value?: string) => value ? `${new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' }).format(new Date(value))} UTC` : 'No successful response yet';
const label = (value: string) => value.replaceAll('_', ' ').replace(/\b\w/g, char => char.toUpperCase());

app.innerHTML = `
  <main class="shell">
    <header class="topbar">
      <a class="brand" href="#" aria-label="Ottawa Fleet home"><span class="brand-mark">OF</span><span>Ottawa Fleet <small>Operator console</small></span></a>
      <div class="top-meta"><span class="live-dot"></span><span>${fixtureMode ? 'Fixture mode · synthetic contract examples' : 'Local operations'}</span><span class="clock" id="updated-at">Connecting to services…</span></div>
    </header>
    <section class="intro">
      <div><div class="eyebrow">FLEET OPERATIONS <span> / </span> OTTAWA</div><h1>Service overview</h1><p>Availability and ride work, using the latest observations reported by each service.</p></div>
      <div class="poll-chip"><span class="pulse"></span> Refreshing every 2 seconds</div>
    </section>
    <section class="summary-grid" aria-label="Fleet summary" id="summary"></section>
    <section class="panel map-panel" aria-labelledby="map-heading">
      <div class="panel-heading"><div><div class="eyebrow">OTTAWA SERVICE AREA</div><h2 id="map-heading">Fleet by zone</h2><p>Approximate neighborhood locations and reported vehicle counts.</p></div><span class="map-live-label"><i></i> Zone view</span></div>
      <div id="fleet-map" class="fleet-map" role="region" aria-label="Ottawa fleet map" data-map-center="45.4215,-75.6972" tabindex="0"></div>
      <div id="map-status" class="map-status" role="status" aria-live="polite">Loading map tiles…</div>
      <div class="map-details"><p class="map-footnote">Approximate zone locations. The v1 API does not provide vehicle GPS positions.</p><ul id="zone-locations" class="zone-locations" aria-label="Vehicle counts by approximate zone"></ul></div>
    </section>
    <section class="panel simulation-panel" aria-labelledby="simulation-heading">
      <div class="panel-heading"><div><div class="eyebrow">OPT-IN SYNTHETIC SCENARIO</div><h2 id="simulation-heading">Route simulation</h2><p>Uses the pinned Lansdowne → Centretown route profile. The browser does not complete rides.</p></div><span class="panel-icon">⌁</span></div>
      <div class="simulation-controls">
        <label>Requests <input id="sim-requests" type="number" min="1" max="20" value="4" /></label>
        <label>Events / second <input id="sim-rate" type="number" min="1" max="4" value="1" /></label>
        <label>Duration (seconds) <input id="sim-duration" type="number" min="1" max="60" value="30" /></label>
        <label>Concurrency <input id="sim-concurrency" type="number" min="1" max="4" value="2" /></label>
        <label>Seed <input id="sim-seed" type="number" min="0" max="2147483647" value="42" /></label>
        <label>Vehicle on map <select id="sim-vehicle"><option value="all">All synthetic vehicles</option></select></label>
      </div>
      <label class="simulation-consent"><input id="sim-opt-in" type="checkbox" /> Start an opt-in synthetic run against the local Simulation Controller.</label>
      <div class="simulation-actions"><button class="primary-button" id="sim-start" type="button" disabled>Start scenario</button><button class="secondary-button" id="sim-stop" type="button" disabled>Stop and drain</button><span id="sim-state" role="status" aria-live="polite">Controller not checked yet.</span></div>
      <p id="sim-summary" class="simulation-summary">No simulation run selected.</p>
      <div id="sim-events" class="simulation-events" aria-live="polite">Run events will appear here when the controller is available.</div>
    </section>
    <section class="panel fleet-panel">
      <div class="panel-heading"><div><div class="eyebrow">INVENTORY</div><h2>Fleet availability</h2><p>Freshness is derived from each vehicle observation and the Fleet API’s <code>as_of</code> clock.</p></div><div id="fleet-state"></div></div>
      <div class="table-wrap"><table><thead><tr><th>Vehicle</th><th>Zone</th><th>Seats</th><th>Availability</th><th>Observed at</th></tr></thead><tbody id="fleet-rows"></tbody></table></div>
    </section>
    <section class="work-grid">
      <section class="panel ride-form-panel">
        <div class="panel-heading"><div><div class="eyebrow">NEW REQUEST</div><h2>Submit a ride</h2><p>Accepted rides are stored before work is queued.</p></div><span class="panel-icon">＋</span></div>
        <form id="ride-form">
          <div class="field-row"><label>Pickup zone<select name="pickup_zone" required>${ZONES.map(zone => `<option value="${zone}" ${zone === 'lansdowne' ? 'selected' : ''}>${label(zone)}</option>`).join('')}</select></label><label>Drop-off zone<select name="dropoff_zone" required>${ZONES.map(zone => `<option value="${zone}" ${zone === 'centretown' ? 'selected' : ''}>${label(zone)}</option>`).join('')}</select></label></div>
          <label>Passengers <input type="number" name="passengers" min="1" max="4" value="2" required /></label>
          <label>Idempotency key <input id="idempotency-key" name="idempotency_key" minlength="8" maxlength="128" pattern="[A-Za-z0-9_-]{8,128}" value="${makeIdempotencyKey()}" required /><small>Reuse this key after an uncertain response to safely reconcile acceptance.</small></label>
          <button class="primary-button" type="submit" ${submitting ? 'disabled' : ''}>${submitting ? 'Submitting…' : 'Submit ride'} <span>→</span></button>
          <p class="form-message" id="form-message" role="status">${escapeHtml(submitMessage)}</p>
        </form>
      </section>
      <section class="panel rides-panel">
        <div class="panel-heading"><div><div class="eyebrow">DURABLE WORK</div><h2>Ride history</h2><p>Queued and processing rides remain visible through API interruptions.</p></div><div id="rides-state"></div></div>
        <div class="ride-list" id="ride-list"></div>
      </section>
    </section>
    <footer><span>Phase 1 local demo · Synthetic fleet and ride data</span><span>Service times are UTC</span></footer>
  </main>`;

function setMapStatus(message: string, state: 'loading' | 'ready' | 'unavailable') {
  const status = document.querySelector<HTMLDivElement>('#map-status');
  if (!status) return;
  status.textContent = message;
  status.dataset.state = state;
}

function initializeFleetMap() {
  const element = document.querySelector<HTMLDivElement>('#fleet-map');
  if (!element || fleetMap) return;
  try {
    fleetMap = L.map(element, {
      center: [45.4215, -75.6972],
      zoom: 12,
      minZoom: 10,
      maxZoom: 18,
      maxBounds: [[45.30, -75.91], [45.56, -75.52]],
      maxBoundsViscosity: 0.8,
      scrollWheelZoom: false
    });
    const tiles = L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', {
      minZoom: 0,
      maxZoom: 19,
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap contributors</a>',
      crossOrigin: true
    });
    const tileKey = (event: L.TileEvent) => `${event.coords.z}/${event.coords.x}/${event.coords.y}`;
    fleetMap.on('movestart', () => {
      failedVisibleTileKeys.clear();
      setMapStatus('Loading map tiles…', 'loading');
    });
    tiles.on('tileerror', event => {
      failedVisibleTileKeys.add(tileKey(event));
      setMapStatus('Map tiles are unavailable. Fleet data and operator controls remain available.', 'unavailable');
    }).on('tileload', event => {
      failedVisibleTileKeys.delete(tileKey(event));
    }).on('tileunload', event => {
      failedVisibleTileKeys.delete(tileKey(event));
    }).on('load', () => {
      if (failedVisibleTileKeys.size > 0) {
        setMapStatus('Some map tiles are unavailable. Fleet data and operator controls remain available.', 'unavailable');
      } else {
        setMapStatus('Map tiles loaded.', 'ready');
      }
    }).addTo(fleetMap);

    ZONE_POINTS.forEach(({ zone, name, point }) => {
      const marker = L.marker(point, { keyboard: true, title: `${name} approximate zone location` })
        .bindPopup('Waiting for fleet observations…');
      marker.addTo(fleetMap!);
      zoneMarkers.set(zone, marker);
    });
    setMapStatus('Loading map tiles…', 'loading');
  } catch {
    setMapStatus('Map is unavailable. Fleet data and operator controls remain available.', 'unavailable');
  }
}

function renderSimulationState() {
  const optedIn = document.querySelector<HTMLInputElement>('#sim-opt-in')?.checked ?? false;
  document.querySelector<HTMLElement>('#sim-state')!.textContent = simulationMessage;
  document.querySelector<HTMLElement>('#sim-summary')!.textContent = simulationRun
    ? `Run ${simulationRun.run_id} · ${simulationRun.state} · ${simulationRun.completed_events}/${simulationRun.issued_events} events confirmed`
    : 'No simulation run selected.';
  document.querySelector<HTMLElement>('#sim-events')!.textContent = simulationRun
    ? `Bounded event history (${simulationEvents.length}/20): ${simulationEvents.map(event => `#${event.sequence} ${event.kind} · ${event.result}`).join(' | ') || 'No events yet'} · ${simulationPositions.length} position reads (${simulationPositions.filter(position => position.freshness === 'fresh').length} fresh)`
    : 'Run events will appear here when the controller is available.';
  document.querySelector<HTMLButtonElement>('#sim-start')!.disabled = !optedIn || simulationBusy || Boolean(simulationRun && ['scheduled', 'running', 'stopping'].includes(simulationRun.state));
  document.querySelector<HTMLButtonElement>('#sim-stop')!.disabled = simulationBusy || !simulationRun || !['scheduled', 'running', 'stopping'].includes(simulationRun.state);
  const select = document.querySelector<HTMLSelectElement>('#sim-vehicle')!;
  const selected = select.value || selectedSimulationVehicle;
  select.innerHTML = '<option value="all">All synthetic vehicles</option>' + simulationVehicles.slice(0, 20).map(id => `<option value="${escapeHtml(id)}">${escapeHtml(id)}</option>`).join('');
  select.value = simulationVehicles.includes(selected) ? selected : 'all';
  selectedSimulationVehicle = select.value;
}

function drawSimulationPositions() {
  if (!fleetMap || document.hidden) return;
  const now = Date.now();
  for (const position of simulationPositions.filter(item => selectedSimulationVehicle === 'all' || item.vehicle_id === selectedSimulationVehicle)) {
    const point = interpolatePosition(position, now);
    let marker = simulationMarkers.get(position.vehicle_id);
    if (!marker) {
      marker = L.circleMarker([point.latitude, point.longitude], { radius: 7, color: '#122018', weight: 2, fillColor: '#b6f06c', fillOpacity: 1 })
        .bindTooltip(`Synthetic vehicle ${escapeHtml(position.vehicle_id)}`);
      marker.addTo(fleetMap);
      simulationMarkers.set(position.vehicle_id, marker);
    } else marker.setLatLng([point.latitude, point.longitude]);
    marker.setStyle({ fillColor: position.freshness === 'fresh' ? '#b6f06c' : '#edc270', opacity: selectedSimulationVehicle === 'all' || selectedSimulationVehicle === position.vehicle_id ? (position.freshness === 'fresh' ? 1 : 0.65) : 0.12 });
  }
  simulationFrame = matchMedia('(prefers-reduced-motion: reduce)').matches ? undefined : requestAnimationFrame(drawSimulationPositions);
}

async function pollSimulation() {
  simulationPollTimer = undefined;
  if (closed || document.hidden || simulationBusy || !simulationRun || !['scheduled', 'running', 'stopping'].includes(simulationRun.state)) return;
  simulationBusy = true;
  try {
    simulationRun = await getRun(simulationRun.run_id);
    simulationEvents = (await getRunEvents(simulationRun.run_id)).items.slice(0, 20);
    if (!simulationRoute) simulationRoute = await getRoute();
    simulationVehicles = simulationVehicles.length ? simulationVehicles : await getVehicles();
    simulationPositions = await mapLimit(simulationVehicles.slice(0, 20), 4, async id => getPosition(id));
    if (fleetMap && simulationRouteLine === undefined) simulationRouteLine = L.polyline(
      simulationRoute.points.map(point => [point.latitude, point.longitude] as L.LatLngExpression),
      { color: '#b6f06c', weight: 3, opacity: 0.8, dashArray: '5 7' }
    ).addTo(fleetMap);
    if (simulationFrame === undefined) simulationFrame = requestAnimationFrame(drawSimulationPositions);
    simulationMessage = `Controller connected · ${simulationRun.state}`;
  } catch (error) {
    simulationMessage = `Simulation unavailable: ${error instanceof Error ? error.message : 'controller or Fleet position API unavailable'}`;
    if (simulationFrame !== undefined) cancelAnimationFrame(simulationFrame);
    simulationFrame = undefined;
    simulationPositions = [];
    simulationMarkers.forEach(marker => marker.remove());
    simulationMarkers.clear();
  } finally {
    simulationBusy = false;
    renderSimulationState();
    if (!closed && !document.hidden && simulationRun && ['scheduled', 'running', 'stopping'].includes(simulationRun.state)) simulationPollTimer = window.setTimeout(() => void pollSimulation(), 2_000);
    else if (simulationFrame !== undefined) { cancelAnimationFrame(simulationFrame); simulationFrame = undefined; }
  }
}

document.querySelector<HTMLInputElement>('#sim-opt-in')!.addEventListener('change', renderSimulationState);
document.querySelector<HTMLSelectElement>('#sim-vehicle')!.addEventListener('change', event => {
  selectedSimulationVehicle = (event.currentTarget as HTMLSelectElement).value;
  simulationMarkers.forEach((marker, id) => marker.setStyle({ opacity: selectedSimulationVehicle === 'all' || selectedSimulationVehicle === id ? 1 : 0.12 }));
});
document.querySelector<HTMLButtonElement>('#sim-start')!.addEventListener('click', async () => {
  if (!document.querySelector<HTMLInputElement>('#sim-opt-in')!.checked || simulationBusy) return;
  simulationBusy = true; simulationMessage = 'Checking the local controller and route profile…'; renderSimulationState();
  try {
    simulationRoute = await getRoute();
    const manifest = scenarioManifest({
      requestCount: Number(document.querySelector<HTMLInputElement>('#sim-requests')!.value),
      rate: Number(document.querySelector<HTMLInputElement>('#sim-rate')!.value),
      durationSeconds: Number(document.querySelector<HTMLInputElement>('#sim-duration')!.value),
      concurrency: Number(document.querySelector<HTMLInputElement>('#sim-concurrency')!.value),
      seed: Number(document.querySelector<HTMLInputElement>('#sim-seed')!.value)
    }, makeIdempotencyKey());
    simulationRun = await createRun(manifest);
    simulationMessage = 'Run accepted by local controller.';
    if (simulationRouteLine) simulationRouteLine.remove();
    simulationRouteLine = undefined;
    if (simulationPollTimer !== undefined) clearTimeout(simulationPollTimer);
  } catch (error) { simulationMessage = `Simulation unavailable: ${error instanceof Error ? error.message : 'controller unavailable'}`; }
  finally {
    simulationBusy = false; renderSimulationState();
    if (simulationRun && ['scheduled', 'running', 'stopping'].includes(simulationRun.state)) simulationPollTimer = window.setTimeout(() => void pollSimulation(), 0);
  }
});
document.querySelector<HTMLButtonElement>('#sim-stop')!.addEventListener('click', async () => {
  if (!simulationRun || simulationBusy) return;
  simulationBusy = true; simulationMessage = 'Requesting stop; accepted effects are draining…'; renderSimulationState();
  try { simulationRun = await stopRun(simulationRun.run_id); simulationMessage = `Stop requested · ${simulationRun.state}`; }
  catch (error) { simulationMessage = `Stop status unavailable: ${error instanceof Error ? error.message : 'controller unavailable'}`; }
  finally {
    simulationBusy = false; renderSimulationState();
    if (simulationRun && ['scheduled', 'running', 'stopping'].includes(simulationRun.state)) simulationPollTimer = window.setTimeout(() => void pollSimulation(), 0);
  }
});
document.addEventListener('visibilitychange', () => {
  if (document.hidden) {
    if (simulationPollTimer !== undefined) { clearTimeout(simulationPollTimer); simulationPollTimer = undefined; }
    if (simulationFrame !== undefined) { cancelAnimationFrame(simulationFrame); simulationFrame = undefined; }
  } else {
    if (simulationPositions.length && simulationFrame === undefined) simulationFrame = requestAnimationFrame(drawSimulationPositions);
    if (simulationRun && ['scheduled', 'running', 'stopping'].includes(simulationRun.state) && simulationPollTimer === undefined) void pollSimulation();
  }
});
matchMedia('(prefers-reduced-motion: reduce)').addEventListener('change', event => {
  if (event.matches && simulationFrame !== undefined) { cancelAnimationFrame(simulationFrame); simulationFrame = undefined; }
  else if (!event.matches && !document.hidden && simulationPositions.length && simulationFrame === undefined) simulationFrame = requestAnimationFrame(drawSimulationPositions);
});

function connectionBadge(snapshot: Snapshot<unknown>): string {
  const names = { loading: 'Loading', empty: 'No records', ready: 'Connected', stale: 'Stale · cached', unavailable: 'Unavailable' };
  return `<span class="state-pill state-${snapshot.state}"><i></i>${names[snapshot.state]}</span>`;
}

function render() {
  const vehicles = fleetSnapshot.items;
  const rides = rideSnapshot.items;
  const availability = vehicles.map(vehicle => deriveAvailability(vehicle.availability, vehicle.observed_at, fleetSnapshot.asOf ?? ''));
  const available = availability.filter(status => status === 'available').length;
  const unknown = availability.filter(status => status === 'unknown').length;
  const pending = rides.filter(ride => ride.state === 'queued' || ride.state === 'processing').length;
  document.querySelector('#summary')!.innerHTML = [
    ['Vehicles observed', vehicles.length.toString(), `${available} available · ${unknown} unknown`, '◉'],
    [fleetSnapshot.state === 'stale' ? 'Last known available' : 'Available now', fleetSnapshot.state === 'loading' || fleetSnapshot.state === 'unavailable' ? '—' : available.toString(), fleetSnapshot.state === 'stale' ? `At ${formatTime(fleetSnapshot.asOf)}` : 'Fresh observations only', '⌖'],
    ['Rides recorded', rideSnapshot.state === 'loading' || rideSnapshot.state === 'unavailable' ? '—' : rides.length.toString(), `${pending} queued or processing`, '↗'],
    ['Fleet as of', fleetSnapshot.asOf ? formatTime(fleetSnapshot.asOf) : '—', 'Fleet API contract clock', '◷']
  ].map(([title, value, note, icon]) => `<article class="metric-card"><div class="metric-top"><span>${title}</span><b>${icon}</b></div><strong>${value}</strong><small>${note}</small></article>`).join('');

  const zoneList = document.querySelector<HTMLUListElement>('#zone-locations')!;
  zoneList.innerHTML = ZONE_POINTS.map(({ zone, name }) => {
    const inZone = vehicles.filter(vehicle => vehicle.zone === zone);
    const stats = inZone.map(vehicle => deriveAvailability(vehicle.availability, vehicle.observed_at, fleetSnapshot.asOf ?? ''));
    const availableInZone = stats.filter(status => status === 'available').length;
    const unknownInZone = stats.filter(status => status === 'unknown').length;
    const hasFleetData = fleetSnapshot.state !== 'loading' && fleetSnapshot.state !== 'unavailable';
    const countLabel = hasFleetData ? `${inZone.length}` : '—';
    const summary = !hasFleetData
      ? fleetSnapshot.state === 'loading' ? 'Waiting for fleet data' : 'Fleet data unavailable'
      : `${countLabel} vehicle${countLabel === '1' ? '' : 's'} · ${availableInZone} available · ${unknownInZone} unknown`;
    const knownSummary = fleetSnapshot.state === 'stale'
      ? `Last known · ${summary} · as of ${formatTime(fleetSnapshot.asOf)}`
      : summary;
    const marker = zoneMarkers.get(zone);
    marker?.setIcon(L.divIcon({
      className: 'map-zone-icon-shell',
      html: `<span class="map-zone-icon map-zone-icon-${zone === 'glebe' ? 'west' : zone === 'lansdowne' ? 'east' : 'center'}" role="img" aria-label="${name}: ${knownSummary}"><b>${countLabel}</b><small>${name}</small></span>`,
      iconSize: [114, 48],
      iconAnchor: [57, 24]
    }));
    marker?.setPopupContent(`<strong>${name}</strong><br>${knownSummary}<br><small>Approximate zone location · no vehicle GPS</small>`);
    return `<li><span class="zone-name"><i class="zone-dot zone-${zone}"></i>${name}</span><span class="zone-count">${knownSummary}</span></li>`;
  }).join('');

  document.querySelector('#fleet-state')!.innerHTML = `${connectionBadge(fleetSnapshot)}<small class="last-success">Last success ${formatTime(fleetSnapshot.lastSuccessAt)}</small>${fleetSnapshot.message ? `<small class="service-error">Fleet API: ${escapeHtml(fleetSnapshot.message)}</small>` : ''}`;
  document.querySelector('#rides-state')!.innerHTML = `${connectionBadge(rideSnapshot)}<small class="last-success">Last success ${formatTime(rideSnapshot.lastSuccessAt)}</small>${rideSnapshot.message ? `<small class="service-error">Ride API: ${escapeHtml(rideSnapshot.message)}</small>` : ''}`;
  const fleetRows = document.querySelector<HTMLTableSectionElement>('#fleet-rows')!;
  if (fleetSnapshot.state === 'loading') fleetRows.innerHTML = '<tr><td colspan="5" class="empty-state">Loading fleet observations…</td></tr>';
  else if (vehicles.length === 0) fleetRows.innerHTML = `<tr><td colspan="5" class="empty-state">${fleetSnapshot.state === 'unavailable' ? `Fleet API unavailable. ${escapeHtml(fleetSnapshot.message ?? '')}` : 'No vehicles reported.'}</td></tr>`;
  else fleetRows.innerHTML = vehicles.map(vehicle => {
    const status = deriveAvailability(vehicle.availability, vehicle.observed_at, fleetSnapshot.asOf ?? '');
    const className = status === 'available' ? 'available' : status === 'reserved' ? 'reserved' : status === 'unknown' ? 'unknown' : 'unavailable';
    const shownStatus = fleetSnapshot.state === 'stale' ? `Last known · ${label(status)}` : label(status);
    return `<tr><td class="vehicle-id"><span class="vehicle-glyph">▰</span>${escapeHtml(vehicle.vehicle_id)}</td><td>${label(escapeHtml(vehicle.zone))}</td><td>${vehicle.seats} seats</td><td><span class="availability status-${className}"><i></i>${shownStatus}</span></td><td><span class="observed-time">${formatTime(vehicle.observed_at)}</span>${status === 'unknown' ? '<small class="age-note">Observation is outside the 30 second freshness window</small>' : ''}</td></tr>`;
  }).join('');
  const rideList = document.querySelector<HTMLDivElement>('#ride-list')!;
  if (rideSnapshot.state === 'loading') rideList.innerHTML = '<div class="empty-state">Loading ride history…</div>';
  else if (!rides.length) rideList.innerHTML = `<div class="empty-state">${rideSnapshot.state === 'unavailable' ? `Ride API unavailable. ${escapeHtml(rideSnapshot.message ?? '')}` : 'No rides have been submitted yet.'}</div>`;
  else rideList.innerHTML = [...rides].reverse().map(ride => `<article class="ride-item"><div class="ride-state-mark state-mark-${ride.state}">${ride.state === 'completed' ? '✓' : ride.state === 'failed' ? '!' : '↻'}</div><div class="ride-main"><div class="ride-title"><strong>${escapeHtml(ride.ride_id)}</strong><span class="ride-status ride-${ride.state}">${label(ride.state)}</span></div><div class="ride-route">${label(escapeHtml(ride.pickup_zone))} <span>→</span> ${label(escapeHtml(ride.dropoff_zone))} <span>· ${ride.passengers} passenger${ride.passengers === 1 ? '' : 's'}</span></div>${ride.vehicle_id ? `<small class="result-note">Assigned ${escapeHtml(ride.vehicle_id)}</small>` : ride.failure_code ? `<small class="result-note failure-note">${label(escapeHtml(ride.failure_code))}</small>` : ''}</div><time>${formatTime(ride.updated_at)}</time></article>`).join('');
  document.querySelector('#updated-at')!.textContent = fleetSnapshot.lastSuccessAt || rideSnapshot.lastSuccessAt ? `Last check ${formatTime(lastPollAt)}` : 'Waiting for first response';
}

function fixturesFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const url = String(input);
  if (url.startsWith('/fleet-api/')) {
    return Promise.resolve(new Response(JSON.stringify({ items: fixtureVehicles, next_cursor: null, as_of: fixtureClock }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
  }
  if (url.startsWith('/ride-api/') && init?.method === 'POST') {
    const request = JSON.parse(String(init.body)) as RideRequest;
    const key = new Headers(init.headers).get('Idempotency-Key') ?? '';
    const existing = (window as typeof window & { __fixtureKeys?: Map<string, { ride: Ride; request: RideRequest }> }).__fixtureKeys ?? new Map<string, { ride: Ride; request: RideRequest }>();
    (window as typeof window & { __fixtureKeys?: Map<string, { ride: Ride; request: RideRequest }> }).__fixtureKeys = existing;
    const prior = existing.get(key);
    if (prior && JSON.stringify(prior.request) !== JSON.stringify(request)) {
      return Promise.resolve(new Response(JSON.stringify({ code: 'idempotency_conflict', message: 'The key is already associated with a different ride request.', request_id: 'fixture-request-001' }), { status: 409, headers: { 'Content-Type': 'application/json' } }));
    }
    let ride = prior?.ride;
    if (!ride) {
      fixtureRideNumber += 1;
      const submittedNumber = fixtureRideNumber;
      ride = { ...request, ride_id: `ride-ui-${String(fixtureRideNumber).padStart(3, '0')}`, state: 'queued', created_at: fixtureClock, updated_at: fixtureClock };
      existing.set(key, { ride, request: { ...request } });
      fixtureRides.unshift(ride);
      window.setTimeout(() => {
        ride!.updated_at = '2026-10-03T12:00:08Z';
        if (submittedNumber <= 2) { ride!.state = 'completed'; ride!.vehicle_id = `vehicle-00${submittedNumber}`; }
        else { ride!.state = 'failed'; ride!.failure_code = 'no_capacity'; }
      }, 8_000);
    }
    return Promise.resolve(new Response(JSON.stringify(ride), { status: 202, headers: { 'Content-Type': 'application/json', Location: `/v1/rides/${ride.ride_id}` } }));
  }
  return Promise.resolve(new Response(JSON.stringify({ items: fixtureRides, next_cursor: null, as_of: fixtureClock }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
}

async function refreshFleet(signal: AbortSignal) {
  try {
    const result = fixtureMode ? { items: fixtureVehicles, asOf: fixtureClock } : await getFleet(signal);
    fleetSnapshot = successfulSnapshot(result.items, result.asOf);
  } catch (error) {
    if (signal.aborted) return;
    fleetSnapshot = failedSnapshot(fleetSnapshot, error instanceof Error ? error.message : 'Fleet API unavailable');
  }
}
async function refreshRides(signal: AbortSignal) {
  try {
    const result = fixtureMode ? { items: fixtureRides, asOf: fixtureClock } : await getRides(signal);
    rideSnapshot = successfulSnapshot(result.items, result.asOf);
  } catch (error) {
    if (signal.aborted) return;
    rideSnapshot = failedSnapshot(rideSnapshot, error instanceof Error ? error.message : 'Ride API unavailable');
  }
}

async function poll() {
  if (closed) return;
  controller = new AbortController();
  const { signal } = controller;
  await Promise.all([refreshFleet(signal), refreshRides(signal)]);
  if (closed || signal.aborted) return;
  lastPollAt = new Date().toISOString();
  render();
  timer = window.setTimeout(poll, 2_000);
}

const form = document.querySelector<HTMLFormElement>('#ride-form')!;
form.addEventListener('submit', async event => {
  event.preventDefault();
  if (submitting) return;
  const data = new FormData(form);
  const request: RideRequest = { pickup_zone: String(data.get('pickup_zone')) as RideRequest['pickup_zone'], dropoff_zone: String(data.get('dropoff_zone')) as RideRequest['dropoff_zone'], passengers: Number(data.get('passengers')) };
  const keyInput = document.querySelector<HTMLInputElement>('#idempotency-key')!;
  const key = keyInput.value;
  submitting = true;
  submitMessage = 'Sending request…';
  form.querySelector<HTMLButtonElement>('button')!.disabled = true;
  document.querySelector('#form-message')!.textContent = submitMessage;
  try {
    const ride = fixtureMode ? await submitRide(request, key, fixturesFetch as typeof fetch) : await submitRide(request, key);
    submitMessage = `Ride accepted: ${ride.ride_id}`;
    keyInput.value = makeIdempotencyKey();
    await refreshRides(controller?.signal ?? new AbortController().signal);
  } catch (error) {
    submitMessage = error instanceof ApiError ? `Ride submission rejected (${error.status}). ${error.message}` : `Acceptance could not be confirmed. Keep this key to retry safely. ${error instanceof Error ? error.message : ''}`;
  } finally {
    submitting = false;
    form.querySelector<HTMLButtonElement>('button')!.disabled = false;
    document.querySelector('#form-message')!.textContent = submitMessage;
    render();
  }
});

window.addEventListener('pagehide', () => {
  closed = true;
  if (timer !== undefined) window.clearTimeout(timer);
  controller?.abort();
  if (simulationPollTimer !== undefined) window.clearTimeout(simulationPollTimer);
  if (simulationFrame !== undefined) cancelAnimationFrame(simulationFrame);
}, { once: true });
if (fixtureMode) window.fetch = fixturesFetch as typeof fetch;
initializeFleetMap();
render();
renderSimulationState();
void poll();
