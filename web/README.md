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
Above 700px, static dots trace the already traveled shape behind each badge,
and a separate chip names the terminal or next station. Map options controls
these cues for the current page session. The phone map is unchanged pending
[HUP-17](https://linear.app/jimmy-li/issue/HUP-17/design-phone-train-direction-cues-and-map-options).

Above 700px, riders can pin two trains from the loaded route in first-pin order
in a dock below the map. One pin owns the active journey; the other stays
compact until its `Show journey` control is used. Clicking a pinned marker or
list button removes it, while a third pin opens the native replacement dialog.
Hover and keyboard focus temporarily preview another train without changing the
active pin. Escape and empty-map clicks dismiss only that preview. A preview
jointly places the popup and next-stop label, including the popup pointer, before
revealing either. If both cannot fit, desktop inspection smoothly tries the
smallest 0.1-level zoom increase up to +1 level, with minimal bounded translation.
Leaving the preview restores the previous view unless the rider navigates the
map. A remaining collision hides the duplicate station label; an unplaceable
preview expands above the dock. At 700px and below, the existing single-train floating/below-
map controls remain; resizing down keeps the active pin and announces removal
of the other.
Pointer hover temporarily hides other markers on the same route; keyboard
focus and pinned trains keep them visible. The highlighted future line ends
at the trip pattern's validated terminal stop, and the whole segment to the
next validated stop has a distinct halo and pulses with that stop's circle
outline. Its name appears beside the circle during preview and selection,
including when normal station labels are unavailable. Next and Toward occupy
separate lines; the next-stop label and circle use route-colored fill with white
text/outline. That styling is isolated from placement so it can be reverted.
Reduced-motion settings show steady emphasis and immediate camera adjustments.

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
- Make the basemap configurable when the app supports a second style or a map
  detail control.

Implementation decisions, trust rules, acceptance criteria, and verification
records live in [`WEBAPP_ARCHITECTURE_PLAN.md`](../WEBAPP_ARCHITECTURE_PLAN.md).
