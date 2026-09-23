# Hol Up web app

## What it is

The web app is a React and TypeScript interface for the Hol Up transit
prototype. It currently shows A-line route geometry, stations, validated train
outcomes, position freshness, and selectable train details. MapLibre renders
the map; the complete station and train information remains available as HTML
when WebGL or the basemap is unavailable.

Train coordinates snap to each accepted polling response. The app does not yet
animate trains between responses.

## Setup and local development

Install Node 24 and npm, then install the locked dependencies:

```sh
cd web
npm ci
```

Run the Go API from the repository root:

```sh
ADDR=127.0.0.1:8080 go run -buildvcs=false ./cmd/api
```

In another terminal, start Vite from `web/`:

```sh
npm run dev
```

Open `http://127.0.0.1:5173`. Vite proxies relative `/api` requests to the Go
API on `http://127.0.0.1:8080`. React component edits use Fast Refresh and CSS
edits update without a full reload. Go source changes require restarting the API
process.

Useful frontend commands:

```sh
npm test          # native Node tests
npm run typecheck # strict TypeScript check
npm run build     # production bundle in dist/
npx playwright install chromium
npm run test:e2e  # deterministic Chromium fixtures
```

The production container builds `web/dist` and serves it through the Go
application. Runtime dependencies are React, React DOM, and MapLibre; Node is
not included in the runtime image.

Train counts distinguish positioned trains, upcoming departures and unavailable
positions. Upcoming trips stay in the list with their predicted departure in New
York time and have no current-location marker. A vehicle sequence disagreement
can be excluded while independent trip predictions supply a position; train
details explain that warning. This does not expand schedule simulation permissions.

Positioned trains use route-colored badges. Individual markers may overlap;
the train list and keyboard navigation remain available for every train. The
user found the counter/chooser confusing and visually disliked it, so the
product decision dated 2026-09-22 prohibits count/counter badges and their
associated marker chooser unless the user explicitly reverses it.

Single-train hover previews the remaining trip, visited stops, destination,
direction, update age, and next station ETA. Click/tap pins the train with a
selection ring at its projected coordinate. Hovering another single train
previews it temporarily. Escape, empty-map clicks, the card close control, and
the selected list button clear selection. Train cards retain their explicit
option to pin details below the map.

The map uses available widescreen width with a 360px sidebar. Below 1024px,
the lists stack below the map.
Offscreen or partially clipped marker targets are omitted; every train remains
available in the list. Zoom scaling/hiding calibration belongs to HUP-16.

Live loading still supports only the A line.

## Feature TODOs

- Add Phase 4 smooth movement between compatible accepted positions.
- Add routes beyond A after their geometry and projection behavior are
  validated.
- Add historical arrival times after a bounded per-run history source exists;
  the current projection intentionally retains no train history.
- [HUP-13: Show route and direction on non-active train markers](https://linear.app/jimmy-li/issue/HUP-13/show-route-and-direction-on-non-active-train-markers).
- Make the basemap configurable when the app supports a second style or a map
  detail control.

Implementation decisions, trust rules, acceptance criteria, and verification
records live in [`WEBAPP_ARCHITECTURE_PLAN.md`](../WEBAPP_ARCHITECTURE_PLAN.md).
