# Browser Web App Implementation Plan

## Goal

Build the smallest useful browser slice for the A line: a user can open the
application, select the A line, see its route branches and stations, view
timestamped estimated train positions, and distinguish current realtime
inference from stale data and schedule simulation.

The browser is a presentation layer over the existing transit API. The server
continues to own reconciliation, projection, freshness, and simulation
decisions. The first version updates train dots when a poll completes; smooth
movement is an MVP follow-up.

This plan covers the browser application and its delivery from the existing Go
container. Alerts, journey planning, the assistant, authentication, and new
backend transit behavior are outside this slice.

## Verified Starting Point

The repository has no frontend yet. The Go modular monolith already exposes the
data needed by this PoC:

- `GET /api/v1/routes/A/geometry` returns route metadata, canonical station
  Points, route-shape LineStrings, service patterns, schedule eligibility, and
  `schedule_version`.
- `GET /api/v1/trains?route_id=A&view=positions` returns every attributable
  realtime outcome and authorized scheduled run, including projected
  coordinates, null-position reasons, provenance, independent ages,
  `position_basis`, movement state, and `schedule_version`.
- Geometry and positions each capture a consistent reconciliation bundle, but
  separate requests can observe different schedule versions.
- `/api/v1/routes` lists routes observed in retained realtime data. It is not a
  static catalog of supported routes, so the browser must keep A selectable
  during empty or unavailable feed states.

No backend contract change is required for the PoC.

## Decisions

### Application stack

Use React, TypeScript, and Vite in `web/`. Integrate MapLibre directly from a
React effect instead of adding a React map wrapper. Use normal React state and
small pure functions; do not add a router, global state library, query library,
component system, or CSS framework.

Runtime dependencies (installed as each phase needs them):

- `react`
- `react-dom`
- `maplibre-gl` in Phase 2

Development dependencies:

- `vite`
- `@vitejs/plugin-react`
- `typescript`
- React and Node type packages
- `@playwright/test` when Phase 3 adds browser tests

Commit `web/package-lock.json`. Keep `web/node_modules/` and `web/dist/`
ignored. Use the Node 24 image line in the container build so local and
container tooling can run TypeScript tests directly.

Expected scripts:

```json
{
  "scripts": {
    "dev": "vite",
    "typecheck": "tsc --noEmit",
    "build": "npm run typecheck && vite build"
  }
}
```

Phase 3 adds `"test": "node --test src/model.test.ts"` with its test file,
and `@playwright/test` with the `"test:e2e": "playwright test"` script.

Do not add a frontend linter in the PoC. TypeScript, focused unit tests, the
production build, and browser tests cover the current failure surface. Add a
linter when repeated style or correctness problems establish a useful ruleset.

### Map renderer and basemap

Use MapLibre GL JS with OpenFreeMap's hosted Positron style. Positron provides a
quiet light map and already omits points of interest. After `style.load`, hide
base-style layers whose ID contains `building` or whose `source-layer` is
`building`. Preserve pale streets, water, parks, and restrained road/place
labels. Disable rotation and pitch; keep the map north-up.

Hardcode the style URL and layer adjustment in the map module for the first
version:

```ts
// TODO: Make the basemap style and detail visibility configurable when a
// second style or user control is required.
```

Do not copy and maintain the full upstream style JSON. Keep OpenFreeMap,
OpenMapTiles, and OpenStreetMap attribution visible. If the hosted style fails,
replace it with a minimal local MapLibre background style and still draw the
API-provided transit layers.

Alternatives considered:

| Option | Result |
| --- | --- |
| Leaflet with raster tiles | Simpler 2D rendering, but labels and buildings are baked into the tiles and cannot be independently hidden. Reconsider if basemap customization stops mattering. |
| MapTiler Light | Good light cartography with managed service options, but introduces an account and browser API key. Reconsider if provider support is required. |
| OpenLayers | Supports broader GIS formats and services than this application needs. |
| Mapbox GL JS | Supports the required rendering, but adds Mapbox account and token requirements without a current Mapbox-specific need. |

References: [MapLibre](https://maplibre.org/maplibre-gl-js/docs/),
[OpenFreeMap](https://openfreemap.org/quick_start/),
[Positron style](https://github.com/hyperknot/openfreemap-styles/tree/main/styles/positron),
and [Leaflet](https://leafletjs.com/).

### Deployment boundary

Keep one deployable application.

Keep `web/` as a self-contained npm subproject in this repository, rather than
a nested Git repository. It can build and test independently while API and UI
changes remain atomic. Use path-filtered CI if separate build timing becomes
useful. Create a separate web repository only when the frontend has independent
ownership or access controls, regularly ships without the Go application, has
a distinct deployment lifecycle, or serves multiple backends. At that point,
publish a versioned API contract and require compatibility checks across both
release pipelines before removing the shared-repository integration gate.

```text
Browser
  |-- GET / and /assets/* -----------------> compiled React files
  `-- GET /api/v1/... ---------------------> existing Go API
                                                  |
                                                  `-> existing in-memory transit state
```

Development uses two local processes:

1. Run the existing Go API on `127.0.0.1:8080`.
2. Run Vite on `127.0.0.1:5173` with `strictPort: true` and proxy `/api` to
   the Go API.

All browser fetches use relative `/api/...` URLs. No CORS configuration is
needed in development or production.

Production adds a Node build stage to `Containerfile`, runs `npm ci`, frontend
checks, and `npm run build`, then copies `web/dist` into the existing non-root
Alpine runtime. Node is not present in the runtime image.

At the Go composition root, use a small standard-library dispatcher. Do not
introduce a `http.ServeMux` that would normalize paths before the API sees them:

- Serve GET/HEAD for `/` and files beneath `/assets/` from `WEB_DIR`, with no
  directory listings. Confine file access to the web directory.
- Route every other request to the existing API handler so its JSON errors and
  method behavior remain intact.
- Default `WEB_DIR` to `web/dist` for repository-root development and set it to
  `/web/dist` in the container.
- A missing web directory returns a normal `404` for UI assets but does not
  prevent the API from starting. The production container build fails if its
  expected frontend output was not produced.
- Keep the PoC at `/`; do not add client-side routes or an SPA fallback.

## Browser Responsibilities

### Page structure

Use a single responsive page:

1. A skip link that moves focus past the interactive map.
2. A header with a labeled native line selector. It contains A for the PoC and
   leaves the interaction ready for later routes.
3. A status region containing data timestamp/age, update state, and any stale,
   simulation, paused, or failure notices.
4. The map and legend.
5. A semantic station list and train list beside the map on desktop and below it
   on mobile.
6. A selected-train detail section.
7. Transit-data and map-provider attribution.

Avoid a drawer or draggable sheet in this slice. CSS grid and one mobile
breakpoint are sufficient.

### Transit visualization

Create three MapLibre GeoJSON sources and update them through `setData`:

- **Route shapes:** include only shape features with usable LineString geometry.
  Draw each branch as supplied. Use a dark casing plus an A-line blue inner
  stroke so the line remains legible over roads. Never connect or sort station
  points to invent geometry.
- **Stations:** include canonical stations with valid Point geometry. Draw
  outlined circles above the route. Show station names at closer zoom levels;
  retain unlocated stations and their reasons in the HTML list.
- **Trains:** include records with finite longitude and latitude. Use filled
  circles for `realtime_prediction` and hollow rings for
  `schedule_fallback`. Use shape plus text in the legend so color is not the only
  distinction.

Fit the camera to valid route geometry after the first successful geometry
load. Add a "Fit A line" button. Polling and train selection must not recenter
the user's map.

Map clicks and HTML train buttons update the same selected-train state. Details
may display only supplied facts: estimated/simulated basis, direction, movement
state, projection time, source/observation age, and null-position or
reconciliation reason. Do not invent a destination or next stop. Do not derive
compass direction from GTFS `direction_id` 0/1.

Use `trip_instance_id` as the stable identity for uniquely reconciled trains.
Keep uncertain records separate with `source_record_id`, falling back to a
deterministic combination of source name, entity ID, and source index. Never
deduplicate records by coordinate or proximity.

### Accessible equivalent

The canvas is supplemental. Stations and trains must remain understandable and
selectable through semantic HTML when the map, WebGL, or basemap is unavailable.

- Use native labels, `select`, and `button` elements with visible keyboard
  focus and touch-sized targets.
- Include every train outcome in the HTML list, including null-coordinate
  records and their `no_position_reason`.
- Preserve focus when polling replaces data. If a selected record disappears,
  return focus to the train-list heading and announce the removal.
- Use a polite live region for meaningful state transitions such as stale,
  simulation, paused, recovered, or failed. Do not announce every poll, marker
  movement, or age tick.
- Provide a "Pause updates" control. While paused, stop requests, freeze the
  displayed snapshot, keep its absolute timestamps visible, and label it
  paused. Resume with an immediate new request.
- Honor `prefers-reduced-motion` for camera movement now and all train animation
  later.
- If WebGL cannot initialize, explain that the visual map is unavailable and
  retain the complete status, station, and train content.

This pause control is part of the accessible PoC because the page updates on a
timer. See [WCAG guidance for auto-updating information](https://www.w3.org/WAI/WCAG22/Understanding/pause-stop-hide.html).

## Data Flow and State Rules

### Geometry lifecycle

Fetch `/api/v1/routes/A/geometry` on initial load and when positions report a
different non-null `schedule_version`.

Accept a geometry response only when its route is A and its required arrays and
coordinates have the expected structure. Preserve the response's version,
eligibility, route metadata, unlocated stations, and unusable shapes in browser
state. Do not reject expired geometry solely because `schedule_eligible` is
false; render it with an explicit schedule warning.

Geometry and position requests are separately atomic. Apply this mismatch rule:

1. Stop displaying train markers when the positions version differs from the
   displayed geometry version.
2. Fetch geometry for the new version through the same data owner; do not start
   a second polling loop.
3. Fetch positions again after geometry succeeds.
4. Publish markers only when the versions match. If they change again, return
   to the normal five-second polling schedule rather than looping immediately.

If either version is null, preserve explicit schedule-unavailable state and
show only data whose geometry relationship is valid. Never plot a position
against known-mismatched geometry.

### Position polling

Use one polling owner in a custom hook or the top-level application effect:

1. Fetch immediately after compatible geometry is available.
2. Set a ten-second `AbortController` timeout.
3. Wait for the attempt to complete.
4. Schedule the next attempt five seconds later.

Requests never overlap. Cleanup aborts the active request and clears its timeout
and next-poll timer. Pair the abort with an effect-generation token so late
responses cannot update state after React Strict Mode cleanup, line changes,
hidden-tab suspension, or geometry-version replacement.

Suspend polling when `document.visibilityState` becomes `hidden`. Keep the last
display explicitly labeled as retained. On return to visible, fetch immediately
and do not relabel it current until a new successful response arrives. A
successful response may itself contain stale upstream data.

Each successful response is authoritative for the current display:

- Replace, rather than merge, the train collection.
- Remove markers for missing records and records whose coordinates became null.
- Retain null records in the HTML list.
- Update MapLibre sources only after response validation and version matching.

### Source age and banners

Treat these states independently:

| State | Trigger | Presentation |
| --- | --- | --- |
| Current response | Latest positions request succeeded | Show projected time, source timestamp, and advancing approximate source age. |
| Stale source | Server reports stale, or locally advanced source age is strictly greater than 60 seconds | Persistent warning that positions use stale source evidence. |
| Simulation | Any displayed outcome has `position_basis: schedule_fallback` | Persistent explanation that marked positions are schedule simulations, even when the source is fresh. |
| Update failure | Request times out, fails validation, or returns non-success | Keep the prior snapshot with "Last received positions, projected at ..." and a retry notice. |
| Paused/hidden | User pauses or the page is backgrounded | Freeze the snapshot and label it retained; resume with a new request. |
| Unavailable | Structured source or schedule error with no usable retained display | Show the specific unavailable condition in place of train markers. |

Anchor source age without trusting the browser wall clock:

1. Use the signed age supplied by the positions response, or calculate the
   equivalent difference between server `projected_at` and source timestamp.
2. Add monotonic elapsed time from the request's `performance.now()` start.
   This slightly overestimates age by including request latency, which is the
   safe direction at the stale boundary.
3. Re-render the visible age on a one-second presentation timer without making
   network requests.
4. Preserve negative values internally. Display a negative value as "source
   clock is approximately N seconds ahead" rather than as a normal age.

Transport failure freezes coordinates. It does not authorize continued browser
simulation or imply that retained simulation remains active. Continue aging the
evidence and change the banner wording to past tense when showing a retained
simulation snapshot.

Do not turn these different results into an empty map:

- A successful response with no train records.
- A successful response containing records that cannot be positioned.
- `503 source_unavailable`.
- `503 schedule_unavailable` or an ineligible schedule.
- Invalid or mismatched response data.
- A failed public basemap request.

### Trust boundary

Define TypeScript types for the fields the UI consumes, then perform lightweight
runtime checks at the fetch boundary for the envelope, enumerated display
values, arrays, timestamps, and finite coordinate pairs. Preserve unknown or
nullable values rather than coercing them to zero, current, stopped, or empty.

Render API strings through React's normal escaping. Do not use raw HTML. Browser
assets contain no MTA, provider, or future assistant credentials. Tile-provider
requests carry only normal map-resource data.

## Implementation Phases

### Phase 1 — Frontend foundation and same-container delivery

Goal: open a responsive React page from the Go application without changing
existing API behavior.

Tasks:

- [x] Create `web/package.json`, lockfile, TypeScript/Vite configuration,
  `index.html`, application entry point, and base CSS.
- [x] Add the single-page semantic layout, A-line selector, honest “not connected
  yet” placeholders, map container, list regions, and attribution footer.
- [x] Add the Vite `/api` development proxy and document the two-process local
  workflow in the repository README.
- [x] Add the Node container build stage and copy `web/dist` into the runtime.
- [x] Add the Go static-file dispatcher at the composition root, preserving all
  current API paths and errors.
- [x] Ignore frontend dependencies and build output.
- [x] Add Go serving tests using a temporary static directory; do not depend on
  a prebuilt `web/dist`.
- [x] Extend the container smoke check to request `/` and one referenced asset.

Phase 1 keeps the shell in one React component with plain CSS and a single
mobile breakpoint. Strict Mode and strict TypeScript are enabled. Node 24 is
declared in `web/.nvmrc` and package engines; resolved versions are locked for
`npm ci`. Static HTML is the smaller alternative, but React supports the
already-planned interactive map and shared selection state. The page makes no
transit requests and displays no loading animation, fabricated records, or map
controls. Map-provider attribution arrives with actual map rendering in Phase 2.

Acceptance:

- `npm ci && npm run build` creates a production bundle.
- Vite development requests reach the Go API through relative `/api` URLs.
- The production container serves its HTML and hashed assets from port 8080.
- Existing API, OpenAPI, metrics, health, readiness, `404`, and `405` behavior
  remains intact.
- The Go API still starts when the local web directory is absent.

Verified on 2026-09-14: `npm ci`, TypeScript checking, the production build,
`go test ./...`, `go test -race ./...`, `go vet ./...`, and the Podman container
smoke test passed. Firefox 155 checks passed against both Vite and Go at 1440,
390, and 320 CSS pixels: layout, keyboard navigation, visible focus, native
skip-link focus, no automatic transit requests, and no console errors. Desktop
and mobile screenshots were visually inspected. A relative browser `/api`
request through Vite returned structured `503 source_unavailable`; an occupied
Vite port failed as configured. A separate Go process started with an absent
`WEB_DIR`, serving UI 404s and working health/API responses. The wider browser
and assistive-technology matrix below remains for the interactive PoC.

### Phase 2 — Accessible A-line geometry

Goal: show A-line context with or without the visual map.

Tasks:

- [ ] Implement the geometry fetch boundary and consumed API types.
- [ ] Initialize one MapLibre instance, hide building detail, disable rotation
  and pitch, and provide the minimal-style fallback.
- [ ] Convert usable shapes and located stations into separate GeoJSON sources.
- [ ] Add route casing/line and station circle/label layers.
- [ ] Fit the initial geometry and add "Fit A line."
- [ ] Render all canonical stations in semantic HTML, including unlocated
  stations and reasons.
- [ ] Add loading, route-not-found, schedule-unavailable/ineligible, basemap
  failure, and WebGL failure presentations.
- [ ] Add the hardcoded-style configuration TODO.

Acceptance:

- All supplied A-line branches render without connecting unrelated stations.
- Located stations appear on the map; every station remains present in HTML.
- Keyboard-only users can select A, bypass the map, read station information,
  and operate "Fit A line."
- Basemap or WebGL failure does not remove route status or station information.

### Phase 3 — Trustworthy positions and update lifecycle

Goal: show train positions without hiding uncertainty or age.

Tasks:

- [ ] Implement the non-overlapping positions loop, abort/generation cleanup,
  page-visibility behavior, and pause/resume control.
- [ ] Implement version matching and coordinated geometry refresh.
- [ ] Convert valid train coordinates to GeoJSON and update one MapLibre source
  per accepted response.
- [ ] Add distinct realtime and schedule-simulation styling and a textual
  legend.
- [ ] Render every outcome in a stable, focus-preserving HTML train list.
- [ ] Synchronize map and list selection with a semantic detail section.
- [ ] Implement monotonic source aging and separate stale, simulation, retained,
  paused, unavailable, and recovery notices.
- [ ] Keep frozen coordinates fixed after transport failure.
- [ ] Add pure TypeScript tests for response decisions and Playwright tests for
  the browser lifecycle using local fixtures.

Acceptance:

- A successful poll replaces markers and list records; null or removed records
  cannot leave ghost markers.
- Simulation remains visibly distinct when the source is either fresh or stale.
- Source age crosses to stale only after 60 seconds; missing and future
  timestamps remain explicit.
- Failures retain the last display with its original projection time and clear
  retained wording, then recover on the next successful response.
- Version mismatch prevents markers from appearing on incompatible geometry.
- Pausing stops requests, resuming fetches current data, and background tabs do
  not accumulate polls.
- Polling does not steal focus, recenter the map, or repeatedly announce changes.

### Phase 4 — MVP smooth movement

Goal: improve motion only after snap-to-poll behavior is proven trustworthy.

Tasks:

- [ ] Match consecutive samples only when run identity, realtime trip, pattern,
  direction, schedule version, shape, and `position_basis` remain compatible.
- [ ] Animate only along a uniquely identified forward section of the supplied
  shape between two accepted endpoints.
- [ ] Bound motion to those endpoints and poll times; never extrapolate.
- [ ] Snap when the path is ambiguous or after missing/null outcomes,
  reconciliation ambiguity, version/path/direction changes, or basis
  transitions.
- [ ] Disable interpolation under `prefers-reduced-motion` and while data is
  retained, paused, hidden, or failed.
- [ ] Measure what fraction of eligible A-line updates can be animated without
  guessing.

The current response supplies coordinates and shape identity but no unambiguous
distance along a shape. Shapes can loop or retrace segments, so nearest-segment
matching is unsafe. If measured coverage is inadequate, add one optional
server-supplied along-shape locator in this phase. The backend already computes
distance along the shape. Keep snap updates as the fallback.

Acceptance:

- Animation never leaves the route geometry or continues past a known sample.
- Every documented continuity break causes an immediate snap/reset.
- Reduced-motion users receive poll-by-poll updates with no train animation.
- Tests cover a normal forward segment, loop ambiguity, basis transition, null
  outcome, route-version change, pause, and failed poll.

## Verification Commands

Run the smallest relevant checks during each phase and the complete set before
calling the browser PoC complete:

```sh
cd web
npm ci
npm test
npm run typecheck
npm run build
npm run test:e2e

cd ..
go test ./...
go test -race ./...
go vet ./...
./test/container_smoke.sh
```

Browser tests use deterministic local API and basemap fixtures. They must not
depend on live MTA or public tile availability. Manually verify current Chrome,
Firefox, and Safari at desktop and narrow mobile widths, keyboard-only use,
one screen-reader pass, reduced motion, and WebGL-disabled behavior.

Live A-line alignment remains an explicit verification gap until checked with a
current MTA schedule archive. Synthetic fixtures prove contract behavior, not
live cartographic quality.

## Deferred Complexity and Adoption Triggers

| Deferred work | Current choice | Trigger to revisit |
| --- | --- | --- |
| Configurable basemaps and detail toggles | One hardcoded quiet style | A second supported style or a user-facing map-detail requirement exists. |
| Additional routes | A remains the only selectable route | Each route's live geometry and projection behavior is validated. |
| Managed or self-hosted tiles | OpenFreeMap public Positron with a local blank fallback | Availability measurements or provider requirements justify an SLA, account, cost, or tile operations. |
| Push transport | Five-second HTTP polling | Poll request load or measured update latency fails an agreed product target; evaluate Go SSE first. |
| Shared frontend state or routing | Local React state on one page | Multiple real screens or cross-panel workflows make ownership unclear. |
| Rich train labels and search | Identity, basis, direction, and timestamps only | Backend contracts provide destinations/next stops, or the product adds station/train search. |
| Universal smooth animation | Snap per poll, then safe partial interpolation | MVP measurement shows ambiguous shape placement prevents acceptable coverage; add an along-shape locator. |

The expensive decisions to preserve are server ownership of transit truth,
explicit provenance and unknown states, schedule-version matching, and stable
scheduled run identity. Map styling, tile provider, and React presentation can
change without altering those contracts.
