# Ottawa Fleet Operator UI

A small TypeScript operator console for synthetic fleet observations and durable ride history. It uses Vite with plain TypeScript and DOM templates because this ticket needs one local page, form and polling loop; a component framework would add runtime and build dependencies without making that flow simpler.

Node.js 20.19 or newer is required by Vite 7.

## Run

```powershell
npm.cmd install
npm.cmd run dev
```

Open the Vite URL, normally `http://localhost:5173`. The default screen calls the Fleet API through `/fleet-api` and the Ride API through `/ride-api`. Vite proxies these to `http://127.0.0.1:8080` and `http://127.0.0.1:8081`, stripping the prefix. The application reverse proxy should provide the same paths for a built UI. `npm.cmd run build` writes the static site to `web/dist`.

Use `http://localhost:5173/?fixture=1` to see explicitly labeled synthetic contract examples and exercise the submit flow before APIs are running. Fixture mode never claims its sample values are live. The UI does not bundle fixed fixture dates into normal API responses.

The fleet map is centered on Ottawa and uses approximate locations for the four
contract zones. The v1 Fleet API provides a zone for each synthetic vehicle,
not coordinates, so map markers and counts are zone summaries rather than live
vehicle positions. The Glebe marker uses Natural Resources Canada's [official
Geographical Names Database point](https://geonames.nrcan.gc.ca/search-place-names/unique?id=FBHRC&wbdisable=true)
for The Glebe (45.4027778, -75.6913889); the other markers are approximate
neighborhood anchors. It uses pinned Leaflet 1.9.4 with the standard
OpenStreetMap raster tile service. Tiles are fetched only as the user views the
map; there is no prefetch or offline download. OpenStreetMap attribution stays
visible on the map. If tiles cannot load, the UI shows a map-specific
unavailable state while the fleet, ride, and submission controls remain
available.

Fleet and ride lists follow `next_cursor`, requesting up to 100 records per page with a 100-page safety cap and repeated-cursor detection. Each API request has a 5-second timeout. Fleet availability is derived against each response's `as_of`; observations older than 30 seconds or in the future display as unknown. Polling uses non-overlapping requests every two seconds. If a poll fails after a successful response, the UI retains that snapshot, marks it stale, labels fleet status as last known, and shows its last-success time and error; before the first successful response, loading and unavailable are distinct. Displayed service times are UTC.

Ride requests use the contract `Idempotency-Key` header and request fields. When a response is uncertain, the entered key remains in place so retrying reconciles the original acceptance. Accepted rides are tracked in API history.

## Checks

```powershell
npm.cmd test
npm.cmd run test:browser
npm.cmd run build
```

The unit tests cover freshness derivation, loading/empty/unavailable/stale snapshots and the submission header/body. Playwright checks the fixture flow, Ottawa map center and attribution, zone counts, tile outage/recovery, and the retained-data labels shown after a Fleet API poll failure.

## Dependency provenance

Dependencies are pinned exactly in `package.json` and `package-lock.json`.
Leaflet 1.9.4 (BSD-2-Clause) provides the map and `@types/leaflet` 1.9.22
(MIT) supplies TypeScript definitions. Vite 7.3.6 (MIT) serves/builds assets,
TypeScript 5.9.2 (Apache-2.0) checks and transpiles source, Vitest 3.2.7 (MIT)
runs behavior tests, and Playwright 1.63.0 (Apache-2.0) drives the browser
smoke tests. Packages are installed from npm; no third-party application code
is copied. Review the npm lockfile before updating these versions.

OpenStreetMap tiles follow the [OSMF tile usage policy](https://operations.osmfoundation.org/policies/tiles/),
including visible attribution, normal browser caching, and no bulk downloads.
