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
component system, or CSS framework for the current PoC. Keep plain CSS for the
page and React components; MapLibre layers retain their separate TypeScript
paint/layout definitions. Reconsider Tailwind only at the adoption triggers
below.

Runtime dependencies:

- `react`
- `react-dom`
- `maplibre-gl`

Development dependencies:

- `vite`
- `@vitejs/plugin-react`
- `typescript`
- React and Node type packages
- `@playwright/test`

Commit `web/package-lock.json`. Keep `web/node_modules/` and `web/dist/`
ignored. Use the Node 24 image line in the container build so local and
container tooling can run TypeScript tests directly.

Current scripts:

```json
{
  "scripts": {
    "dev": "vite",
    "test": "node --test --test-isolation=none src/*.test.ts",
    "test:e2e": "playwright test",
    "typecheck": "tsc --noEmit",
    "build": "npm run typecheck && vite build"
  }
}
```

Phase 3 extends the existing Node and Playwright suites with position-state
and polling tests. Both test commands are implemented.

Do not add a frontend linter in the PoC. TypeScript, focused unit tests, the
production build, and browser tests cover the current failure surface. Add a
linter when repeated style or correctness problems establish a useful ruleset.

### Product decision: overlapping train markers (2026-09-22)

The user found the HUP-14 counter/chooser confusing and visually disliked it.
Do not use count or counter badges, or their associated marker chooser, for
co-located train markers unless the user explicitly reverses this decision.
The grouping/chooser implementation is removed; its
[archived algorithm reference](docs/optimization/train-marker-grouping.md) is
historical only. Individual train markers may overlap. This accepts the current
pointer reachability limitation for markers hidden beneath another marker; the
complete train list and keyboard navigation remain available as the accessible
ways to reach every train. HUP-16 remains deferred and owns any later measured
decluttering policy; it does not authorize restoring the rejected counter/chooser
pattern.

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

Create separate MapLibre GeoJSON sources for static geometry:

- **Route shapes:** include only shape features with usable LineString geometry.
  Draw each branch as supplied. Use a dark casing plus an A-line blue inner
  stroke so the line remains legible over roads. Never connect or sort station
  points to invent geometry.
- **Stations:** include canonical stations with valid Point geometry. Draw
  outlined circles above the route. Show station names at closer zoom levels;
  retain unlocated stations and their reasons in the HTML list.
- **Trains:** render records with finite longitude and latitude as native HTML
  controls over the map. Use compact route-letter badges with a slate outer
  ring: filled for `realtime_prediction` and hollow for `schedule_fallback`.
  Individual markers may overlap; see the persistent product decision above.
  Hover/focus uses a dashed ring. Click/tap pins the badge with an amber
  ring at its anchor, without introducing another collision through displacement. A second
  hover temporarily overrides the pinned view and restores it on mouse-out.
  Pointer hover alone temporarily hides other same-route markers visually;
  their buttons remain keyboard accessible, and keyboard focus or pinning
  restores them. Trip inspection highlights the validated shape only from the
  train to its pattern's terminal stop; other branches remain neutral. The
  next-stop segment has a contrasting halo and pulses with the stop-circle
  outline. Show its name beside the circle independently of basemap glyphs;
  hide the label when the stop is offscreen. Reduced-motion settings retain
  steady emphasis.

Fit the camera to valid route geometry after the first successful geometry
load. Add a "Fit A line" button. Polling preserves the user's camera. Inspection
first tries alternate card anchors away from every visible route, then accepts
route overlap before using bounded temporary panning. Active cards stay on the
map. Pinned details move below the map only through the explicit card control
and can be restored on-map. Dismissal restores the preceding camera,
unless the user deliberately navigated. Hover-over-selection has its own camera
snapshot so leaving the preview restores the pinned view.

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

Use one polling owner in `useTrainData`:

1. Fetch immediately after the initial geometry attempt completes, including failure.
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

Status: complete.

Tasks:

- [x] Implement the geometry fetch boundary and consumed API types.
- [x] Initialize one MapLibre instance, hide building detail, disable rotation
  and pitch, and provide the minimal-style fallback.
- [x] Convert usable shapes and located stations into separate GeoJSON sources.
- [x] Add route casing/line and station circle/label layers.
- [x] Fit the initial geometry and add "Fit A line."
- [x] Render all canonical stations in semantic HTML, including unlocated
  stations and reasons.
- [x] Add loading, route-not-found, schedule-unavailable/ineligible, basemap
  failure, and WebGL failure presentations.
- [x] Add focused Playwright coverage for loading, ready/warning, failure/retry,
  keyboard access, and responsive layout using local fixtures.
- [x] Add the hardcoded-style configuration TODO.

Acceptance:

- All supplied A-line branches render without connecting unrelated stations.
- Located stations appear on the map; every station remains present in HTML.
- Keyboard-only users can select A, bypass the map, read station information,
  and operate "Fit A line."
- Basemap or WebGL failure does not remove route status or station information.

Implemented and verified on 2026-09-14:

- The validated fetch boundary preserves timeout/cancellation during body reads;
  an effect-generation guard suppresses stale success and failure after cleanup.
  Coordinate ranges match the Go boundary. The complete alphabetical directory
  uses English numeric collation and station IDs to resolve equal names.
- `RouteMap` owns the map, north-up controls, first usable bounds fit, resize
  observation, resource deadline, fallback, and cleanup. Sources retain supplied
  shape order and string IDs (`promoteId`). Labels start at zoom 13; the local
  fallback needs neither tiles nor glyphs. At Phase 2 completion, the A-only screen had no geometry refresh path;
  Phase 3 below adds source updates without replacing the map.
- MapLibre 6.9 is bundled through Vite, including its explicit worker import
  under `/assets/`. Hosted-style errors and the initial deadline activate the
  plain-background fallback. MapLibre uses its own local-font fallback if glyphs
  are unavailable. No tile proxy or API changes.
- Native Node tests, strict TypeScript, production build, and focused Chromium
  fixture tests pass. Reviewed canvas snapshots show branches and station circles
  at desktop and narrow widths.
  Focused Go tests and `go vet` pass; the container smoke test verifies JavaScript,
  CSS, and the actual bundled map worker through Go's asset dispatcher. The
  container build also runs the complete Go test suite. No frontend linter is
  configured; TypeScript remains the existing static check.
- Live API validation used supplemented schedule
  `supplemented:2:3760d3dc9e801486f1f0be26bb3ba5f7204bfbd57678bcee21bb80eb8748298f`,
  validated at `2026-09-14T17:19:19Z`, eligible at inspection: 78 canonical stations,
  25 usable shapes, zero missing station coordinates and zero unusable shapes.
  Chromium rendered it over hosted Positron without console errors. The live
  screenshot was reviewed for geographic alignment and visible branches.
  Installed Firefox 155 also rendered the live map with one canvas, all 78
  stations, no overflow at 1440/390/320 pixels, working native skip-link focus,
  and no console errors; its directory screenshot was reviewed.

Remaining verification gaps: a hands-on Safari check and a screen-reader pass
have not been performed in this Linux environment. Firefox verification used
WebDriver BiDi and screenshot inspection, not a complete manual interaction
pass. Live inspection is a single schedule snapshot, not an audit of every
service pattern or future schedule publication. Phase 3 verification is recorded below.

### Phase 3 — Trustworthy positions and update lifecycle

Goal: show train positions without hiding uncertainty or age.

Status: complete.

Tasks:

- [x] Implement the non-overlapping positions loop, abort/generation cleanup,
  page-visibility behavior, and pause/resume control.
- [x] Implement version matching and coordinated geometry refresh.
- [x] Project valid train coordinates into a native HTML marker overlay per
  accepted response.
- [x] Add distinct realtime and schedule-simulation styling and a textual
  legend.
- [x] Render every outcome in a stable, focus-preserving HTML train list.
- [x] Synchronize map and list selection with a semantic detail section.
- [x] Implement monotonic source aging and separate stale, simulation, retained,
  paused, unavailable, and recovery notices.
- [x] Keep frozen coordinates fixed after transport failure.
- [x] Add pure TypeScript tests for response decisions and Playwright tests for
  the positions polling lifecycle using local fixtures, extending the Phase 2
  browser suite.

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

Implemented in three increments on 2026-09-15:

1. **Validated polling and cleanup.** `useTrainData` owns initial and manual
   geometry requests, coordinated refreshes, positions polling, retained
   snapshots, cancellation generations, visibility, and presentation ticks.
   Positions start after the initial geometry attempt even if it fails. Each
   fetch has a cleared ten-second deadline covering JSON body reading; each
   cycle schedules its successor five seconds after completion. Pausing,
   hiding, and unmounting abort active work and clear timers. Resume/visibility
   restoration fetch immediately when permitted and retain the snapshot label
   until a validated success. Runtime checks cover consumed Go/OpenAPI fields,
   enums, nullable timestamps, signed ages, coordinate pairs, per-record route
   and schedule consistency, and identity collisions. Invalid responses cannot
   replace accepted data; valid empty collections do replace it. Source and
   schedule unavailability, timeout, network, HTTP, and validation failures have
   distinct notices.
2. **Version-safe map updates.** Geometry and positions retain separate versions.
   Only matching non-null versions publish markers. A different non-null
   positions version requests geometry and, after success, positions again;
   continued disagreement or refresh failure waits for the next five-second
   cycle. Missing geometry can recover manually or from later positions.
   `RouteMap` keeps its map instance, fits first usable bounds or the explicit
   control, allows reversible inspection camera adjustments, and reinstalls the latest accepted sources after
   fallback. It suspends train interactions before source changes, serializes source writes,
   awaits MapLibre's promises and loaded source tiles at idle, and checks a
   revision before revealing. Obsolete style/source completions cannot reveal
   trains. An HTML marker overlay supplies high-contrast route badges with filled
   realtime inference and hollow schedule simulation treatments. Selection uses
   the matching shape, pattern and cumulative distance to highlight visited and
   upcoming route segments; replacement removes missing/null markers. The HTML
   panel states how many outcomes have publishable coordinates
   and groups the supplied reason codes with plain-language explanations. Blocking
   reasons distinguish rejected trip identity (`start_time_conflict`),
   rejected stop evidence (`stop_id_sequence_conflict`), and a matched run lacking
   a safe interval around the current time (`insufficient_timing`).
3. **Accessible outcomes and trust signals.** Every outcome has a keyed native
   selection button and supplied evidence/reasons, including unpositioned and
   uncertain records. Identities use namespaced run IDs, source record IDs, or
   JSON source/entity/index tuples. Stable identity ordering avoids moving
   focused DOM nodes when server order changes. Map and list share selection;
   removal clears details, focuses the train heading, and announces removal.
   Signed ages advance from server evidence plus monotonic time since request
   start, independently for source and observation. Missing/future times stay
   explicit; source evidence becomes stale strictly above 60 seconds or when
   reported stale. Pause/hidden states suspend ticks, resume recomputes elapsed
   age, and transport failures freeze coordinates while evidence ages.
   Retained simulations use past tense. Live regions contain transitions, not
   poll timestamps or age ticks. No browser movement prediction was added.

Verification on 2026-09-15:

- 37 native Node tests pass, including the original 29 geometry tests and new
  validation, identity, replacement, version, signed-age, stale-boundary, and
  fetch/body-read checks. Strict TypeScript and production build pass. No
  frontend linter is configured; the existing MapLibre bundle-size warning
  remains.
- 36 deterministic Chromium tests pass. The original geometry tests use explicit
  empty train fixtures. New coverage includes production train rendering and
  a test-only Vite harness using the real App and MapLibre under Strict Mode:
  slow requests, exact completion-relative retries, body-read timeout,
  cancellation, late completions, pause/hidden/unmount, recovery, null/removed
  markers, repeated versions, geometry failure, delayed source writes and style
  fallback, shared selection, reordered-list focus, escaped API strings,
  independent trust signals, camera preservation, and reduced motion.
- Layout assertions cover 1440, 390, and 320 CSS pixels, including WebGL failure.
  Desktop and narrow screenshots with realtime and simulated trains were
  visually inspected. Original geometry screenshot baselines still pass.
- `go test ./...`, `go test -race ./...`, `go vet ./...`, and the Podman container
  smoke check pass. Smoke verification includes the bundled JavaScript, CSS,
  MapLibre worker, and unchanged API error responses through the Go server.

Live follow-up on 2026-09-15 sampled 37 A-line outcomes with no publishable
coordinates: 15 `insufficient_timing`, 10 `start_time_conflict`, and 12
`stop_id_sequence_conflict`. An immediately earlier sample briefly had one
positioned train, so this records observed feed behavior rather than a stable
coverage metric.

Verification limits: Safari and a hands-on screen-reader pass are unavailable
in this Linux session. Phase 3 browser verification uses Chromium; the earlier
Firefox check covered Phase 2 only. Visibility events and delayed body/source
completions are controlled fixtures, not browser throttling measurements.
The live follow-up did not repeat basemap verification. The test-only harness is
served by the development test server and is absent from `dist`.

### Phase 3 map interaction follow-ups

- [ ] **HUP-14 — reconsider co-located markers.** The counter/chooser
  implementation was rejected on 2026-09-22 because it was confusing and
  visually disliked. Markers now overlap; the train list and keyboard navigation
  remain available for every train. Do not restore that pattern unless the user
  explicitly reverses the product decision.
- [x] **HUP-15 — docked two-train comparison.** Production decision dated
  2026-09-29: above 700px, keep at most two ordered pins from the currently
  loaded route in a dock below the map. A reducer owns ordered pins, the
  persistent active journey, and a pending third-pin replacement. Hover/focus
  previews are transient. The subsequent tooltip decision replaces the original
  no-camera-movement rule: jointly measure/place the popup, its pointer, and the
  two-line next-stop label before reveal. Exhaust fixed positions, then smoothly
  try desktop zoom increments of 0.1 up to +1 level with minimal translation
  bounded to one-quarter viewport per axis. Restore on dismissal unless the rider
  navigates; polling must not accumulate zoom. Hide the duplicate label if no
  safe pair fits; when the preview itself cannot fit, expand it above the dock.
  Phone camera controls remain unchanged. Next-stop colors are an independently
  reversible styling change. Native modal replacement preserves
  the chosen slot and restores trigger focus. Accepted polling snapshots refresh
  card evidence without reordering, remove missing trains, and promote the newest
  remaining pin; retained failures preserve pins. Incompatible schedule geometry
  suppresses only markers and journey overlays. Phone layouts retain one selected
  train and responsive collapse keeps the active pin with a live announcement.
  Three-pin dock overflow/ordering, phone multi-pin controls, and simultaneous
  multi-route comparison are deferred until explicitly requested.
- [ ] **HUP-16 — Zoom-responsive map decluttering.** Measure desktop/mobile
  zoom values, then set shared thresholds for scaled train/station/route
  rendering and for hiding train controls at low zoom. Include the low-zoom
  marker density and viewport-edge clipping. It must preserve the 2026-09-22
  decision against count/counter badges and their associated chooser UI. Hide
  HUP-13 direction dots and labels whenever train markers are hidden.

The page remains fluid with responsive gutters, a flexible map and 360px
sidebar at 1024px and above, and stacked content below. Map height is
`clamp(480px, 70dvh, 800px)` wide and `clamp(320px, 55dvh, 560px)` stacked.
Resize updates the existing map without refitting the camera. Mobile web is
supported with 44px controls and cooperative map gestures.

Rollback verification (2026-09-22): native frontend tests, strict TypeScript,
production build, and all 60 Chromium browser tests pass. Regression coverage
asserts separate coincident marker targets, no counters/chooser, keyboard/list
selection of obscured trains, and focus retention after reordered polling.
Responsive layout checks pass at 1920, 1440, 1024, 768, 390, and 320px; desktop
and mobile screenshots were inspected. The collision algorithm exists only in
the inactive documentation reference. No frontend linter is configured; Safari
and hands-on screen-reader verification remain unavailable.

Superseded HUP-14 verification (2026-09-21; retained as historical evidence,
not a specification to restore the rejected UI):

- All 51 native frontend tests pass, including deterministic hit-target grouping,
  exact/transitive overlaps, projection changes, and viewport boundaries.
- The full 66-test Chromium suite passed. After the final large-text spacing
  adjustment and an additional constrained-map case, all 11 affected layout
  checks passed (67 browser cases existed in the suite then).
- Browser coverage includes pointer/touch/keyboard selection, focus retention,
  polling additions/removals, zoom regrouping, mixed-route component geometry,
  camera preservation, docked details, and delayed source/style replacement.
- Reviewed screenshots at 1920, 1440, 1024, 768, 390, and 320px; checked crowded
  groups and 200% text with a long destination label. Updated and verified the
  intentional map layout screenshot baselines.
- Strict TypeScript and production build pass. No frontend linter is configured;
  the existing MapLibre bundle-size warning remains. `git diff --check` passes.
- Safari and hands-on screen-reader checks remain unavailable. No live
  multi-route verification or device performance measurements are claimed.

### Phase 3 follow-up — tolerant conflict handling

Status: implemented; Phase 4 animation remains separate.

Decision: improve position coverage by treating selected realtime/static
conflicts as warnings when stronger evidence still identifies one safe result.
The relaxation is narrow:

- For `start_time_conflict`, an exact realtime `trip_id` or unique origin/path
  suffix may remain matched when every other supplied identity field (including
  NYCT direction) agrees with one active scheduled instance in the captured
  schedule. Omitted optional fields retain existing semantics. The weaker
  origin/route/direction rule remains strict; malformed start times are rejected.
  Tolerant candidates participate in all existing uniqueness checks, including
  duplicate records/sources, incomplete snapshots, ambiguous service dates, and
  conflicting ownership. Accepted matches retain `matched` / `unique_identity`.
- For `stop_id_sequence_conflict`, prefer a `stop_id` only when it occurs once in
  the matched trip and the supplied sequence exists. Check duplicates and order
  across all provisional associations in original feed order (`InputIndex`).
  Accepted corrections retain the ignored sequence disagreement as a warning.
  This correction applies only to trip updates; vehicle associations remain strict.
  For fresh predictions, a vehicle `stop_id_sequence_conflict` whose ID identifies
  one scheduled occurrence is excluded from projection, with warning
  `vehicle_stop_sequence_conflict_ignored`. Its stopped-state and segment constraints
  are excluded together. The immutable bundle retains the original conflict and
  continues to veto stale simulation. Valid ID/sequence pairs still identify
  repeated occurrences. Predictions and input identifiers are unchanged.
- Keep a train unpositioned when identity is ambiguous, the stop ID is absent or
  repeated, resolved stop order conflicts, versions differ, or coordinates would
  require extrapolation. `insufficient_timing` is missing evidence rather than a
  conflicting identifier, so this decision does not relax it.
- A fresh validated future departure at the first scheduled occurrence is
  `not_started`, with nullable `predicted_departure_at`, unknown movement and no
  coordinates. Current predicted dwell or accepted fresh stopped evidence prevents
  this classification. Evaluate it before geometry lookup, and reevaluate on each
  server projection; the browser does not independently start movement. Assignment
  flags are not used to infer lifecycle. List/details show "Upcoming" and the
  departure in America/New_York time. Counts separate positioned, upcoming and
  unavailable records; upcoming trips are excluded from the failure summary.
- Publish a coordinate only through the existing validated shape interpolation
  or dwell paths. Every positions record includes a sorted, deduplicated
  `reconciliation_warnings: string[]`, including `[]`. Warnings remain separate
  from `reconciliation_reason` and `no_position_reason`; accepted conflicts can
  coexist with `insufficient_timing`. Immutable bundle copies include nested
  warning collections. Authorized stale simulation retains the warnings, which
  do not independently authorize coordinates or simulation. The vehicle-exclusion
  warning applies only while fresh predictions permit that exclusion.
- The shared `TrainInfo` renders a labeled warning section in both list and
  selected details, explaining which evidence was preferred. Blocking conflicts
  retain rejection wording. The browser accepts omitted warning arrays for
  compatibility, rejects present fields that are not arrays of strings, and
  renders unknown codes as escaped generic text. Retention keeps warnings;
  a clean replacement clears them. No dependencies or runtime policy layer
  were added; shared backend rules apply consistently across routes.

Acceptance:

- Fixture tests prove the tolerated cases resolve uniquely and retain warning
  text; neighboring ambiguous cases remain null.
- The frontend continues to reject malformed coordinates and version mismatch,
  and visibly distinguishes a tolerated conflict in train details.
- Record positioned/unpositioned counts before and after the change using the
  same deterministic fixture and a labeled live sample. Do not define success
  from a single volatile feed response.

Verification record (2026-09-15):

- `TestConflictCoverageFixture` uses evaluation and source time
  `2026-09-02T16:05:00Z` and schedule version
  `regular:2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`.
  The baseline was run before changing reconciliation behavior. The same 14
  fixture inputs now assert these outcomes:

  | Fixture group | Before positioned / null | After positioned / null |
  | --- | --- | --- |
  | Clean exact and suffix matches | 2 / 0 | 2 / 0 |
  | Exact and suffix start-time conflicts | 0 / 2 | 2 / 0 |
  | Unique stop IDs with conflicting sequences | 0 / 1 | 1 / 0 |
  | Both tolerated conflicts | 0 / 1 | 1 / 0 |
  | Both conflicts without timing | 0 / 1 | 0 / 1 |
  | Seven neighboring unsafe cases | 0 / 7 | 0 / 7 |
  | **Total** | **2 / 12** | **6 / 8** |

  Unsafe cases cover weaker identity, simultaneous NYCT direction conflict,
  malformed start time, unknown sequence, absent stop ID, duplicate associations,
  and original feed order conflicts. Before: six `start_time_conflict`, three
  `stop_id_sequence_conflict`, and one each of `invalid_start_time`, `unknown_stop`,
  and `conflicting_stop_order` blocked positioning. After: one each of
  `insufficient_timing`, `start_time_conflict`, `nyct_direction_conflict`,
  `invalid_start_time`, `unknown_stop`, `stop_id_sequence_conflict`,
  `duplicate_stop_update`, and `conflicting_stop_order`. Warning counts are four
  `start_time_conflict` and three `stop_id_sequence_conflict`; two corrected stops
  in one record emit only one stop warning.
- Reconciliation tests cover exact/suffix uniqueness across scheduled instances,
  service dates, records and sources, incomplete sources, conflicting ownership,
  repeated stop occurrences, strict vehicles, and immutable warning copies.
  Timing tests run both with and without a start warning; simulation vetoes
  retain cancellation, observation, ownership, relationship, and replacement
  checks. The ZIP/protobuf integration fixture exercises ingestion normalization,
  both warnings through HTTP, authorized stale simulation, failed refresh
  retention, and warning removal on clean recovery under the same run ID.
- OpenAPI examples are checked against actual DTO/projection output, including
  empty warnings without a schedule and both warnings in realtime and simulation.
  HTTP tests also assert warnings with `insufficient_timing` and empty arrays on
  clean/absent scheduled runs.
- `go test ./...`, `go test -race ./...`, `go vet ./...`, 38 native frontend tests,
  TypeScript checking, the production build, all 38 Chromium Playwright tests,
  and `test/container_smoke.sh` under Podman pass. Browser tests cover optional,
  empty, multiple, unknown/escaped and malformed warnings, positioned and null
  trains, selected details, retained simulations, and clean replacements.
  Existing malformed-coordinate, version-mismatch, polling, geometry, and layout
  checks pass. No frontend linter is configured; the existing MapLibre chunk-size
  build warning remains.

Labeled live observations from the local API against public MTA feeds:

| Sample | Evaluation time (UTC) | ACE source timestamp (UTC) | Positioned / null | Reconciliation reasons | No-position reasons | Warning counts |
| --- | --- | --- | --- | --- | --- | --- |
| Before implementation | 2026-09-15T16:35:32Z | 2026-09-15T16:35:23Z | 0 / 38 | `unique_identity`: 29; `start_time_conflict`: 9 | `start_time_conflict`: 9; `stop_id_sequence_conflict`: 13; `insufficient_timing`: 16 | None; field absent in baseline API |
| After implementation | 2026-09-15T16:45:11Z | 2026-09-15T16:44:41Z | 0 / 36 | `unique_identity`: 36 | `stop_id_sequence_conflict`: 20; `insufficient_timing`: 16 | `start_time_conflict`: 7; `stop_id_sequence_conflict`: 0 |

Both samples used eligible schedule version
`supplemented:2:c2b19bdbe33f064f9c1af38f3b27a210291db6de16fc080e48321595772c3613`.
Live access succeeded after enabling networking for the local API; no live
access blocker remained. Samples contain different realtime records roughly ten
minutes apart, so they establish neither a controlled improvement nor regression.
The after-sample demonstrates accepted start conflicts but **no live position
coverage gain**: remaining stop/timing failures still block coordinates. The
deterministic fixtures establish the intended coverage improvement and preserved
rejections. Browser warning behavior was verified with controlled API responses;
this follow-up did not repeat a live basemap or non-Chromium browser inspection.

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
npx playwright install chromium
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

The Phase 2 verification record above includes one current schedule and live
basemap inspection. Synthetic fixtures establish contract and rendering behavior;
repeat the separate live inspection when assessing a new schedule or map style.

## Deferred Complexity and Adoption Triggers

| Deferred work | Current choice | Trigger to revisit |
| --- | --- | --- |
| Configurable basemaps and detail toggles | One hardcoded quiet style | A second supported style or a user-facing map-detail requirement exists. |
| Additional routes | A remains the only selectable route | Each route's live geometry and projection behavior is validated. |
| Managed or self-hosted tiles | OpenFreeMap public Positron with a local blank fallback | Availability measurements or provider requirements justify an SLA, account, cost, or tile operations. |
| Push transport | Five-second HTTP polling | Poll request load or measured update latency fails an agreed product target; evaluate Go SSE first. |
| Shared frontend state or routing | Local React state on one page | Multiple real screens or cross-panel workflows make ownership unclear. |
| Tailwind CSS | Plain CSS plus separate MapLibre style definitions | Repeated responsive/component styling becomes costly across several screens, or an agreed design-token system needs framework enforcement. |
| Rich train labels and search | Identity, basis, direction, and timestamps only | Backend contracts provide destinations/next stops, or the product adds station/train search. |
| Universal smooth animation | Snap per poll, then safe partial interpolation | MVP measurement shows ambiguous shape placement prevents acceptable coverage; add an along-shape locator. |
| Stagehand browser automation | Deterministic Playwright tests from Phase 2 onward | A later workflow requires AI-directed browser interaction that fixed Playwright scenarios cannot express reliably. |

The expensive decisions to preserve are server ownership of transit truth,
explicit provenance and unknown states, schedule-version matching, and stable
scheduled run identity. Map styling, tile provider, and React presentation can
change without altering those contracts.
