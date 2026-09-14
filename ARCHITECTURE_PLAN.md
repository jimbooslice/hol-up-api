# NYC Subway Simulation and Grounded Delay Assistant

## Summary

Build an interview-ready transit platform as a Go modular monolith with a TypeScript/MapLibre UI. The repository already has a Go API that polls all eight feeds in MTA's Subway Realtime catalog, decodes protobuf messages, rejects feed headers beyond a configurable future-clock tolerance, retains per-source last-known-good snapshots, and serves route and train endpoints. It also exposes OpenAPI, health/readiness, per-feed metrics, safe structured failure/recovery logs, a local LGTM environment, and a container smoke test. Static GTFS ingestion, schedule–realtime reconciliation, route geometry, and train projection are implemented; alerts, journey planning, capture persistence, the UI, and the assistant remain future work.

The implemented realtime and schedule projection foundation supplies map data. Next add the browser, deterministic line status and journey impact, then a deferred LLM explanation layer.

```text
MTA schedules ─┐
MTA realtime ──┼─> Go ingestion/state ─> projection, status, journey ─> HTTP API ─> UI
MTA alerts ────┘                                │
                                                └─> assistant ─> LiteLLM
```

The LLM is neither the source of truth nor the route planner. If it or LiteLLM fails, the map, journey results, and structured disruption facts remain available.

## Implementation Status

The realtime foundation was verified at commit `4244cbd`; realtime semantic completion below was verified by commit `f59ac0f` and its regression tests.

- **Delivered realtime foundation:** all eight independently refreshed MTA subway realtime feeds; atomic last-known-good snapshots; route ownership and partial/unavailable coverage; stale/unavailable states; route and train APIs; health, readiness, metrics, and OpenAPI endpoints; safe structured refresh and recovery logs; container build/smoke verification; and configurable future feed-header timestamp rejection with retained-data recovery.
- **Delivered realtime semantics:** nullable prediction/delay presence; separate feed, trip-update, and vehicle freshness; bidirectionally unique vehicle matching; complete normalized stop events and relationships; retained canceled/deleted trips; and feed/entity/receipt provenance. The compact API and active counts are derived from this retained state. Sequence-only stops remain internal and can be associated with scheduled occurrences; original feed order is retained separately from compact sorting.
- **Delivered schedule ingestion (Phase 1):** independent startup/hourly supplemented ingestion; bounded ZIP/CSV validation; New York service calendars, DST and extended-hour times; atomic immutable dataset/index versions; supplemented retention and regular fallback; a validated atomic disk cache; and source selection/publication/failure/recovery logs. Configuration and persistence decisions are documented in `README.md`.
- **Delivered internal reconciliation:** immutable schedule/source bundles, exact and MTA suffix matching, explicit ambiguity and stop outcomes, validated replacement authority with freshness checks at lookup, and serialized publication callbacks. Compact HTTP responses and readiness remain unchanged. Geometry and the positions view use the captured bundle; explicit simulation decisions govern stale and absent scheduled runs.
- **Not started:** alerts and operational facts, journey routing, browser UI, assistant and access control, capture/replay persistence, and standalone architecture decision records.

The checkboxes below describe implementation status. A checked item has repository evidence; an unchecked item may include a note describing partial work without claiming completion.

## Architecture and Domain Changes

### Core transit platform

- [ ] Keep one Go application with transport-neutral modules for schedule ingestion, realtime reconciliation, projection, alerts, journey planning, operational facts, and assistant orchestration. Serve the compiled UI from the same container. **Partial:** the current realtime API is a Go modular monolith in one container; schedule, reconciliation, and projection modules are also internal; alerts, journey modules, assistant orchestration, and UI remain deferred.
- [x] Keep MTA adapters at the boundary. The core domain must not depend on HTTP, protobuf, CSV, or a persistence framework. **Delivered:** protobuf/HTTP work stays in `internal/ingest`; schedule HTTP/ZIP/CSV and disk caching stay in `internal/scheduleingest`. `internal/realtime`, `internal/schedule`, and `internal/reconcile` are transport- and persistence-neutral.
- [x] Validate a feed header timestamp against local receipt time with a small allowed skew before publication. Reject materially future timestamps so a bad upstream clock cannot block later valid updates. **Delivered:** `MTA_GTFS_RT_MAX_FUTURE_SKEW` defaults to `30s`, supports strict zero, is validated before startup, and applies to all eight loaders.
- [x] Preserve field presence when normalizing GTFS-Realtime. An omitted delay or prediction remains unknown; do not expose it as zero or on-time. `prediction_delay_seconds` is nullable in the API. **Delivered:** omitted delays stay null, explicit zero remains zero, absolute-only predictions do not invent delay, and `NO_DATA` blocks prediction and trip-delay fallback.
- [x] Track freshness separately for the feed, trip update, and vehicle observation. Do not call an old vehicle state fresh merely because it arrived in a new feed; stale movement state is `unknown`. **Delivered:** separate timestamps and freshness are exposed; missing or future observations are unavailable, and stale/unavailable vehicle observations suppress movement.
- [x] Match vehicles to trip instances only when the association is unambiguous in both directions. Keep movement unknown when a partial descriptor could apply to more than one instance. **Delivered:** compatible descriptors must be unique in both directions across the full feed before route filtering; unmatched and ambiguous states remain explicit.
- [x] Retain complete ordered stop predictions, stop relationships, cancellations, and source provenance in normalized state. Derive compact train responses from this model rather than discarding data needed by station arrivals, journey overlays, and skip-stop handling. **Delivered:** arrival/departure events, optional sequences, relationships, canceled/deleted trips, start times, and source/entity/receipt provenance are retained. Fully sequenced stops are sorted; otherwise feed order is preserved. Compact train responses and active counts exclude canceled/deleted trips and trips without an identified served stop.
- [x] Publish all eight realtime feeds independently. A known route query depends only on its owning feed. Preserve additional observed routes; return their available trains with partial coverage when another feed has not loaded, but do not return an empty result for an unobserved route until every feed has loaded. Expose `complete`, `partial`, or `unavailable` coverage and per-source configured routes, timestamps, and freshness. Keep process liveness separate from complete-source freshness.
- [x] Dispatch health and static-document requests before reading the realtime snapshot. Sort published snapshots once; do not copy and sort all trains for every inexpensive request.
- [x] Load MTA’s supplemented GTFS at startup and hourly, retaining a last-known-good dataset atomically. Use regular GTFS only when no valid supplemented dataset is available. The supplemented feed covers most, not all, changes for the next seven calendar days and is updated hourly. **Delivered:** retained supplemented preference, 24-hour default validation age plus service-coverage eligibility, independent polling, and separate seven-day planning coverage. [MTA developer resources](https://www.mta.info/developers)
- [x] Parse GTFS by CSV headers. Resolve service in `America/New_York` using `calendar.txt` and `calendar_dates.txt`, including daylight-saving boundaries and times beyond 24:00. Include timezone data in the runtime image or embed it in the Go binary. **Delivered:** exception-only calendars, optional time presence, GTFS noon-minus-12-hours instants, and embedded timezone data.
- [ ] Build and publish the schedule dataset and its routing index as one compatible version. Bound ZIP decompression, define schedule-cache expiry, and retain capture data for a defined window. **Partial:** immutable dataset and trip/route/service/ordered-stop lookup indexes publish together; ZIP/CSV and cache reads are bounded; one active archive persists atomically without renewing validation age on restart. Journey routing indexes and capture history remain deferred.
- [x] Define MTA-specific realtime/static reconciliation before schedule fallback or journey overlays. Realtime trip IDs do not necessarily match static trip IDs; unmatched and ambiguous outcomes must remain explicit. Apply NYCT trip-replacement periods so static trips absent from a complete realtime replacement period are not resurrected as active service. [MTA subway GTFS-Realtime reference](https://www.mta.info/document/134521)
- [x] Project trains along GTFS shapes between stop events, but never describe an interpolated point as GPS-observed. Stale feed age and schedule simulation are explicit in the API when reconciliation authorizes the run; the browser warning remains UI work.

Expose these semantic fields in projected train and journey responses:

- `position_basis`: `realtime_prediction` or `schedule_fallback`
- `movement_state`: `inferred_moving`, `reported_stopped`, or `unknown`
- `freshness`: `fresh`, `stale`, or `unavailable`
- explicit nullable prediction time and delay when the feed lacks them
- source timestamp and observation timestamp where applicable

### Internal reconciliation decisions

`internal/reconcile.Reconcile` receives one immutable schedule version, all
normalized source snapshots and configured route ownership, an evaluation time,
and the existing freshness limits. It returns a separate immutable bundle with
schedule eligibility, provenance, matching outcomes, stop associations, rejection
reasons, and replacement rules. Mutable inputs and collection lookups are copied.
Missing/ineligible schedules grant no fallback authority. Geometry and the opt-in
positions response consume this bundle. Compact responses and readiness continue
to use their existing source-state behavior.

Matching uses explicit service dates, otherwise the schedule's candidate-date
calculation, and checks coverage and calendar activity for each candidate.
Priority is exact static ID, exact origin/path suffix, then exact origin, route,
and direction when path data is absent. Static prefixes are opaque: both the
historical documented prefix and `BSP26GEN-A055-Sunday-00_000950_A..S74R` are
supported. Origin units are integer 600 milliseconds; no rounding, nearest-time
matching, aliases, or guessed identifier structure is used. Unknown IDs may
match exactly or through separately supplied origin/route/direction fields.
Every supplied identity field must agree with the selected candidate. Generic
GTFS direction IDs are compared to static direction IDs; NYCT compass direction
is compared to the parsed static suffix, without assuming a universal mapping
between the two direction systems. A supplied NYCT direction that cannot be
verified remains invalid. Stronger conflicting or ambiguous matches cannot
fall through. [MTA identifier definition](https://www.mta.info/document/134521)

Each complete source is evaluated before route selection. The candidate graph
must be unique in both directions, including cancellations and unidentified
records, regardless of match priority or iteration order. Filtered inputs cannot
establish full-source uniqueness. Stop ID and sequence must name the same
scheduled occurrence; sequence-only updates resolve directly and ID-only updates
require a unique occurrence. Repeated stops, duplicate updates, missing identity,
and conflicting original feed order retain explicit outcomes. Predictions,
cancellations, `SKIPPED`, and `NO_DATA` remain untouched; associations are separate.

Replacement rules require NYCT extension version `1.0`, an unfiltered full
snapshot, the configured route owner, and at least one representable boundary.
Explicit zero differs from omission. Intervals include start, exclude end, and
leave omitted endpoints unbounded. Empty, reversed, or overflowing ranges are
invalid. Valid same-source windows form a union. Malformed siblings disable the
affected route; missing route identity disables that source's replacement
authority. Rule validation is independent of prediction contents.
[GTFS TimeRange semantics](https://gtfs.org/documentation/realtime/reference/#timerange)

`Bundle.Fallback` uses the captured schedule's first-stop departure and GTFS
service-day conversion. Only future instances inside fresh authoritative windows
can be suppressed for absence. A unique non-canceled counterpart is active even
without compact next-stop fields. Invalid/ambiguous candidates protect affected
instances; unlocalizable records protect the supplied routes or entire source.
Conflicting route fields protect both declared and parsed routes. Multiple
configured owners cannot establish absence. Store windows and counterpart
outcomes rather than enumerating unbounded future service. Every lookup rechecks
schedule eligibility and source freshness, so published results lose suppression
authority after an outage. Fallback eligibility expresses internal authority,
not a request to render an inferred prediction.

`Bundle.SimulationDecision` separately authorizes rendering. A stale uniquely
matched run can simulate under the same trip-instance ID and retain its stale
realtime provenance. This supersedes the earlier draft recommendation to disable
simulation during outages. Permission requires an eligible captured schedule,
active service, exactly one configured owner, and retained complete source data
with usable metadata. Known cancellations and uncertain candidate/source/route
associations remain vetoes after staleness. Absent runs remain suppressed inside
retained replacement windows after departure or freshness expiry; a retained
unique counterpart counts as present. Simulation stays inside scheduled first
departure/final arrival and uses the existing maximum schedule age (24h default).
Fresh records without timing or geometry stay null, with an explicit reason.

The geometry endpoint returns canonical station Points, shape LineStrings, and
ordered stop occurrences per pattern. The positions view uses distance along the
captured shape and never labels coordinates as GPS-observed. Both responses carry
schedule versions; clients refetch geometry on mismatch and restart interpolation
after nulls, ambiguity, path/direction/identity changes, or basis transitions.
Older parser-schema caches require a successful new schedule fetch during rollout;
reinterpreting bytes cannot renew cached validation time.

Application wiring owns the schedule store, source registry, and result store.
Every successful realtime publication and every schedule publication (including
cache restoration and unchanged-content freshness renewal) triggers a run. One
mutex is acquired before capturing inputs and the clock and held through atomic
bundle publication. A schedule published during a run triggers a following run;
readers use the bundle's schedule, never an independently loaded version. Network
requests run outside this mutex; failed refreshes preserve existing inputs.
Reconciliation stays in memory and uses linear scans. Add indexes or asynchronous
scheduling only if measured durations delay refresh processing.

One INFO completion event includes trigger, schedule metadata/eligibility,
source provenance, outcome counts, and duration. DEBUG trip events add identity,
rule, candidates, rejections, and stop outcomes without payloads or upstream URLs.
`LOG_LEVEL` defaults to INFO and accepts case-insensitive DEBUG/INFO/WARN/ERROR;
invalid configuration fails before polling or serving with a fixed application
error that does not echo the value.

### Alerts and operational facts

- [ ] Poll the MTA subway alert feed every 30 seconds and atomically replace the normalized in-memory alert set when the source is a full dataset.
- [ ] Normalize alert ID, feed timestamp, active periods, route/stop/trip selectors, plain-English header and description, created/updated timestamps, and MTA’s free-text alert type.
- [ ] Use only the plain `en` translation. Treat all alert text as untrusted data; do not render or send `en-html` content as HTML.
- [ ] Match alerts deterministically: a route selector applies to its route; a route-and-stop selector applies only when that station is traversed; a trip selector applies only to the resolved trip instance. Preserve and distinguish `current`, `future_planned`, and `expired` alerts.
- [ ] Represent each answerable statement as an `OperationalFact` with a response-local ID, typed structured value, `reported`, `predicted`, `scheduled`, or `derived` basis, source record ID, source timestamp, effective period, and freshness.
- [ ] Treat an MTA alert as authoritative for a reported cause. Realtime lateness can establish an estimated delay but never a cause. [MTA alert documentation](https://www.mta.info/document/90881)

### Deterministic journey service

- [ ] Build a versioned in-memory routing index whenever a schedule version becomes active. Each journey request reads one schedule/index version for its full evaluation.
- [ ] Present parent stations in autocomplete and expand them into direction-specific platform stops during routing. Derive transfers from `transfers.txt` when supplied and from platforms sharing a parent station; do not invent street-level transfers absent from GTFS.
- [ ] Use a round-based public-transit routing algorithm such as RAPTOR to compute earliest-arrival itineraries, capped at four transfers.
- [ ] Overlay only fresh, unambiguously reconciled GTFS-Realtime predictions without modifying the underlying schedule. Return each leg’s route, boarding/alighting stations, scheduled and predicted times, delay basis, matched facts, source freshness, and limitations.
- [ ] Do not remove or reroute service solely from free-text alert categories. Show impacts and MTA-provided alternatives as reported facts.
- [ ] Default `departure_at` to now. Allow future queries only within the active supplemented-schedule horizon; future results use schedules and planned alerts rather than realtime predictions.

### Assistant orchestration and access control

- [ ] Keep assistant orchestration in Go. Connect to LiteLLM through a Go `ModelGateway` interface and an OpenAI-compatible HTTP endpoint. LiteLLM owns model routing, provider credentials, usage tracking, and budgets.
- [ ] Use a bounded, non-agentic pipeline:
  1. Validate session, quota, message/history bounds, and a structured line, journey, or trip subject before any expensive work.
  2. Resolve the subject through line-status or journey-impact services.
  3. Build a complete verified fact bundle and deterministic template fallback.
  4. Make one non-streamed model call with an assistant-specific timeout, concurrency semaphore, and circuit breaker.
  5. Accept only fact selections and presentation structure permitted by the bundle; render factual claims from verified structured facts. A valid fact ID alone does not validate a claim, number, or cause.
  6. Retry once only for malformed output. Return the template fallback for other model errors or unsupported output.
- [ ] Do not give the model arbitrary tools, URLs, database access, filesystem access, or raw MTA feeds. Return escaped plain text with citation markers rather than model-generated HTML or arbitrary links.
- [ ] Require a model to pass the structured-output contract suite before it may serve the public `transit-assistant` alias. Keep LiteLLM outside core readiness.
- [ ] Keep chat history in browser memory and send at most six recent turns. Do not persist prompts or answers in the Go application; verify LiteLLM and provider logging settings separately before any public release.
- [ ] Invite beta uses opaque invite tokens stored as hashes in secret configuration and exchanged for secure, HTTP-only signed session cookies. Anonymous access uses signed anonymous sessions with privacy-preserving IP hashing.
- [ ] Enforce quotas atomically before journey calculation or model calls. Bound session issuance, request body size, message size, history count and size, concurrent work, and in-memory counter retention. Trust forwarded client IP headers only from configured proxies.
- [ ] Initial limits are 1,000 characters per message, six history turns, 500 generated tokens, one in-flight request per session, and four assistant requests globally per Go instance. Anonymous limits are five requests/hour and ten/day; beta limits are twenty/hour and one hundred/day. Use a LiteLLM virtual key restricted to the assistant alias with a mandatory global budget/cutoff.

### Persistence and evolution triggers

- [ ] Keep current schedule, realtime, alerts, journey index, quota counters, and conversations in memory for the initial single-replica deployment. Maintain only the last-known-good schedule cache on local disk. **Partial:** schedule/realtime state and metrics are process-local; the active schedule cache persists through a load/save interface. Alerts, journey indexes, quotas, and conversations remain deferred.
- [ ] Add compressed raw schedule, realtime, and alert recording behind a `SnapshotStore` interface after the live A-line slice. Captures must retain source identity, receipt time, source timestamp, and schema/version metadata for deterministic replay.
- [x] Add Redis or another shared `QuotaStore` only when multiple Go replicas require shared quota accounting. Add a conversation database only when persistent user history is an explicit feature. **Current decision:** neither has been introduced because its trigger has not occurred.
- [x] Add durable workflows only when replay, backfill, or import jobs require durable resume across process restarts. Split the assistant into a separate Go service only if measured model traffic affects map latency, provider egress needs isolation, or teams require independent releases. **Current decision:** neither has been introduced because its trigger has not occurred.
- [x] Do not add a vector database: live operational facts are structured data and the assistant scope excludes general documentation Q&A.

## Rust Evaluation

Rust is a learning opportunity, not a reason to create a second production service. The Go modular monolith remains the production architecture until a distinct responsibility justifies another deployment boundary.

| Candidate | Decision | Adoption trigger |
| --- | --- | --- |
| Capture/replay CLI | Preferred first Rust project; defer until capture data exists. The CLI validates recorded payloads and metadata, safely reports corrupt/truncated captures, and replays them through the Go ingestion path without reimplementing transit logic. | Investigating or replaying captured outages becomes recurring work. |
| Realtime connection service | Defer. It may own WebSocket lifecycle, fan-out, bounded queues, slow-client handling, heartbeats, and graceful shutdown, while Go retains authoritative transit state and authorization policy. | Bidirectional browser messaging becomes a product requirement, or measurement shows connection handling materially affects core API latency. |
| Quota or abuse-enforcement service | Keep in Go. Identity, session issuance, authorization, request validation, quota accounting, and admission to expensive work belong together. A Rust service would add a network and shared-state failure boundary without solving policy correctness. | Multiple independently deployed applications need one shared enforcement point and the existing shared-store approach is demonstrably inadequate. |
| LiteLLM wrapper | Exclude. LiteLLM already owns model gateway responsibilities. | A distinct provider-egress or policy boundary emerges. |

For initial browser updates, use HTTP polling. Smooth map movement comes from client-side interpolation between timestamped updates. If server-to-browser push becomes useful, evaluate Go SSE before WebSockets because current events are one-way. Keep polling as a fallback. Add Rust WebSockets only after the stated trigger and prove bounded memory, slow-client isolation, authorization, reconnection recovery, graceful shutdown, and HTTP fallback.

Do not introduce Redis solely to connect a future Rust service to Go. Redis Pub/Sub is at-most-once delivery; durable job completion and replayable events require an explicit retained-event design. [Redis Pub/Sub delivery semantics](https://redis.io/docs/latest/develop/pubsub/#delivery-semantics)

- [ ] Record ADRs for the modular-monolith boundary, realtime fact semantics, MTA reconciliation and replacement periods, deterministic routing, LLM-as-presentation-layer rule, deferred persistence, and Rust adoption triggers. **Open:** these decisions are described here, but no ADR files exist yet.

## APIs, UI, and Access

### Public interfaces

- [x] `GET /api/v1/routes`
  - Return route IDs from every loaded source with aggregate coverage and per-source status; return `503 source_unavailable` only when no source has loaded.
- [x] `GET /api/v1/routes/{route_id}/geometry`
- [x] `GET /api/v1/trains?route_id=A`
  - Return stale or fresh data from the route's owning source. Additional observed routes may return partial results; an unobserved route returns `503` until every source has loaded and then returns an empty list.
- [x] `GET /api/v1/trains?route_id=A&view=positions`
  - Return complete attributable trip-update outcomes and authorized schedule simulation; preserve null outcomes and stable scheduled identity.
- [ ] `GET /api/v1/stations?query=...` — deferred; geometry already contains canonical stations.
- [ ] `GET /api/v1/routes/{route_id}/status?at=...`
- [ ] `POST /api/v1/journeys/impact`
  - Input: canonical origin/destination station IDs and optional departure time.
  - Output: resolved itinerary, leg projections, matched facts, source coverage/freshness, and limitations.
- [ ] `POST /api/v1/assistant/queries`
  - Input: message, subject (`line`, `journey`, or selected `trip`), and at most six prior turns.
  - Output: resolved subject, complete fact bundle, answer text, cited fact IDs, and answer mode `model` or `template_fallback`.
- [x] `GET /healthz`, `GET /readyz`, and `GET /metrics`
  - Liveness means the process is running. Readiness is healthy when any configured realtime source is fresh and reports complete or partial source coverage separately; assistant health never makes core transit readiness fail.

Successful realtime responses include `coverage` and `sources`; each source reports its stable name, configured route ownership, nullable source timestamp, and freshness. A `503 source_unavailable` response includes the error plus the same coverage and source details. Return `422` for unresolved or ambiguous stations, `429` for quota exhaustion, and structured source errors. If transit facts succeed but the model fails, return `200` with `template_fallback`.

### UI changes

- [ ] Add line-status badges and alert cards before chat; they remain useful without an LLM.
- [ ] Add a desktop side drawer and mobile bottom sheet labeled as an AI-generated operational explanation.
- [ ] Require a visible structured subject: a selected line, origin and destination, or selected train. Use station autocomplete rather than silently resolving ambiguous names.
- [ ] Show the resolved itinerary and deterministic fact cards alongside assistant output, including source, effective period, basis, coverage, freshness, and “as of” timestamp.
- [ ] Say “No active MTA alert was found” instead of claiming “There are no delays.” When no alert supports a cause, state that the cause cannot be determined from available data.
- [ ] Show a stale-data or schedule-simulation banner whenever relevant realtime source age exceeds one minute.

## Delivery Sequence

- [x] **Phase 1 — Realtime correctness:** timestamp-skew validation, nullable predictions/delays, observation freshness, bidirectional matching, complete normalized stop/cancellation/provenance state, partial-feed coverage, and low-cost health endpoints are implemented and covered by regression tests. All eight feed groups, safe structured lifecycle/failure/recovery logs, metrics, and the local [Grafana LGTM environment](ops/lgtm/README.md) are delivered. Application tracing/profiling, alerts, and custom dashboards remain deferred.
- [x] **Phase 2a — Schedule ingestion and reconciliation:** atomically publish supplemented/regular schedules and lookup indexes; retain validated NYCT replacement periods; reconcile realtime trip instances and stop updates with explicit ambiguity; and publish provenance-rich immutable results.
- [x] **Phase 2b — A-line projection:** canonical station records in route geometry and opt-in train positions with explicit `realtime_prediction` or `schedule_fallback` basis. Station search remains deferred; synthetic geometry is verified and live A-line alignment remains unmeasured.
- [ ] **Phase 3 — Capture and replay:** record bounded raw snapshots through `SnapshotStore`, add a controllable clock, and replay rush-hour and outage scenarios. **Partial:** loaders and HTTP handlers accept controllable clocks in tests; raw capture, replay, and a shared simulation clock remain. Revisit the Rust CLI when these captures have practical value.
- [ ] **Phase 4 — All routes and public map:** add supported route selection, accessibility-conscious UI, container deployment, metrics, and MTA compliance notices using the complete realtime feed set. **Partial:** the backend covers all realtime feed groups, is containerized, exports metrics, and provides generic route geometry/positions; operational alignment validation, UI, accessibility work, and compliance notices remain.
- [ ] **Phase 5 — Operational status:** ingest service alerts and ship deterministic line-status cards and APIs.
- [ ] **Phase 6 — Journey impact:** build the routing index, station selectors, realtime overlay, and origin-to-destination impact UI.
- [ ] **Phase 7 — Local assistant:** implement the Go model adapter, deterministic fact rendering, template fallback, chat drawer, and offline evaluation suite.
- [ ] **Phase 8 — Gated rollout:** enable invite beta, observe grounding, cost, latency, and abuse controls, then enable anonymous quotas after rollout tests pass.
- [x] **Phase 9 decision — Realtime transport reevaluation:** current HTTP polling remains appropriate because no push requirement or measured connection bottleneck exists. Consider Go SSE, then a Rust connection service only if its documented trigger occurs.

## Remaining PoC and MVP Work

### PoC: one trustworthy end-to-end transit slice

The smallest convincing PoC is an A-line vertical slice that proves realtime facts can be reconciled with schedules and rendered on a map. Complete these in order:

1. **Complete:** realtime semantic foundation: nullable prediction/delay presence, observation timestamps and freshness, bidirectional trip/vehicle ambiguity handling, and complete ordered stop/cancellation/provenance state.
2. **Complete:** ingest supplemented GTFS with regular-feed fallback, bounded ZIP/CSV parsing, `America/New_York` service-day handling, atomic last-known-good publication, and compatible schedule/index versioning.
3. **Delivered:** reconcile MTA trip instances and replacement periods before schedule fallback, preserving unmatched and ambiguous outcomes.
4. **Delivered:** route geometry with canonical stations and opt-in train positions with explicit `realtime_prediction` versus `schedule_fallback` basis and stale-data behavior.
5. Add a minimal accessible browser map using HTTP polling, route selection, train positions, source age, and a visible stale/simulation banner. Keep it in the existing container.
6. Record the expensive-to-reverse decisions as ADRs and retain automated Go, race, vet, API, and container checks for the vertical slice.

PoC acceptance: a user can open the app, select the A line, see stations and timestamped train positions, distinguish inferred realtime from schedule simulation, and observe correct stale/unavailable behavior without an LLM.

### MVP: useful all-route operational product

After the PoC proves the data model and reconciliation rules:

1. Expand geometry, projection, route selection, accessibility work, and MTA compliance notices to every supported route.
2. Add bounded capture/replay if live reconciliation failures cannot be diagnosed reliably from structured logs and fixtures; otherwise keep it deferred until that trigger occurs.
3. Ingest alerts, normalize untrusted plain-English content, match selectors deterministically, expose route status, and render line-status badges and alert cards.
4. Build station autocomplete and the versioned journey index, then ship deterministic journey impact with fresh realtime overlays, planned alerts, limitations, and schedule-version consistency.
5. Add the assistant only after deterministic facts and journey impact are stable: one bounded LiteLLM call, verified fact selection, escaped rendering, template fallback, contract tests, and no effect on core readiness.
6. Add invite sessions, quotas, proxy trust rules, request/concurrency bounds, provider budget cutoff, privacy/logging verification, and rollout measurements before public assistant access.

MVP acceptance: all supported routes have a usable map and source status; active alerts and journey impacts remain useful without the model; assistant failure falls back to deterministic output; and public access has bounded cost and abuse controls.

## Test Plan and Acceptance Criteria

- [x] Test timestamp skew rejection, atomic replacement, and out-of-order recovery. For partial feeds, test one-feed startup failure, stale last-known-good retention, route-specific freshness, an observed extra route with partial coverage, unobserved-route `503` until all sources load, readiness with any fresh source, structured source errors, health/static-document handling without loading train snapshots, and the exact complete feed-to-route registry with unique ownership.
- [x] Test omitted/zero delay, absolute arrival/departure predictions, `NO_DATA`, stale/missing/future trip and vehicle observations, ambiguity in both directions (including route filtering and duplicate descriptors), canceled/deleted trip retention, skipped/sequence-only stops, complete stop ordering, nested snapshot immutability, provenance, and compact API/metric filtering.
- [x] Test header-based GTFS parsing, service exceptions, daylight-saving boundaries, times beyond 24:00, bounded ZIP extraction, atomic schedule/index replacement, regular-feed fallback, MTA trip reconciliation, and replacement periods. **Partial:** deterministic ingestion, immutable/concurrent reader, fallback/recovery, retained-data, cache restart/expiry/corruption, and independent-startup tests are implemented in `internal/schedule`, `internal/scheduleingest`, and `cmd/api`. Reconciliation, replacement authority, immutable result reads, serialized captures, publication renewal, and the offline A-line ingestion fixture are covered in `internal/reconcile`, `cmd/api`, and `integration`.
- [x] Test shape parsing and atomic publication, pattern/anchor immutability, branches and repeated stops, bent-polyline interpolation, null outcomes, live-to-stale simulation and recovery under one run ID, replacement/cancellation/ambiguity vetoes, geometry-version changes, and offline geometry/positions HTTP contracts.
- [ ] Test alert replacement, active-period boundaries, future alerts, route/stop/trip matching, plain-text extraction, and untrusted alert content.
- [ ] Test direct journeys, transfers, no-route outcomes, midnight service days, updated arrivals, missed transfers, stale overlays, unmatched realtime trips, and schedule/realtime version consistency.
- [ ] Test the assistant with a fake LiteLLM server: valid fact selections, valid-but-irrelevant citations, invented numbers or causes, malformed output, timeouts, `429`, cancellation, circuit opening, and template fallback.
- [ ] Test quota enforcement before expensive work, concurrent requests, session issuance, expiry, proxy trust boundaries, request/body/history limits, and provider budget cutoff.
- [ ] When Rust is adopted, test corrupt capture handling and deterministic replay for the CLI; or bounded memory, slow clients, authorization, reconnect recovery, shutdown, and polling fallback for a connection service. **Deferred until an adoption trigger occurs.**
- [ ] Verify provider credentials never reach browser assets, prompts are absent from default logs where controllable, and assistant failure never makes transit readiness unhealthy. **Deferred until browser and assistant code exists.**

Assumptions: the map will be publicly accessible; the initial deployment is a single Go replica; assistant answers are limited to live operational information; chat persistence and document RAG remain out of scope; no current requirement exists for bidirectional browser messaging; and MTA branding assets will not be used without the applicable license.
