# A-line geometry and train projection

Status: implementation contract, updated for the confirmed decisions on 2026-09-13.
The backend slice is implemented in the schedule, reconciliation, projection, and
HTTP modules. The browser remains outside this task. Repository findings below
describe the baseline that motivated the changes.

## Outcome and repository findings

Add route geometry and a complete projection view to the existing Go application.
Keep schedule-derived geometry in the immutable schedule version, let reconciliation
own permission to simulate, and compute positions at request time from one captured
bundle. All domain lookups accept a route ID; A is the initial acceptance fixture.

The existing foundation supports this design, with four material qualifications:

- [Schedule data](internal/schedule/schedule.go) already retains station parents,
  coordinates, trip shape IDs, ordered stop occurrences, calendars, and service-day
  conversion. Shape points and stop-time shape distances are absent.
  [The decoder](internal/scheduleingest/decode.go) checks unused ZIP entries for
  size/checksum integrity; it does **not** validate `shapes.txt` rows. Its current
  test fixture deliberately contains non-CSV shape content.
- [Reconciliation](internal/reconcile/reconcile.go) captures all retained trip
  updates, including unidentified records, and exposes resolved occurrences.
  `Bundle.Fallback` is an internal eligibility/suppression lookup, not permission
  to render. It returns `Eligible: true` for stale sources, uncertain counterparts,
  and missing replacement authority. It only suppresses absent future departures;
  cancellation alone is not an unconditional rendering veto. These distinctions
  are also documented in [the existing plan](ARCHITECTURE_PLAN.md#internal-reconciliation-decisions).
- [The compact HTTP selection](internal/httpapi/httpapi.go) uses
  [Train.Active](internal/realtime/realtime.go), which requires an identified next
  stop. Using it for projection would omit sequence-only and empty-stop updates.
  HTTP currently does not consume reconciliation bundles.
- [Realtime ingestion](internal/ingest/ingest.go) retains vehicle match, timestamp,
  and derived movement, but discards the vehicle's independent current-stop identity
  and status. It also loses whether an unusable trip timestamp was originally supplied.
  Projection needs those distinctions when choosing a stop or freshness policy.

Working assumptions: "every active realtime record" refers to the retained
TripUpdate population, including `UnidentifiedTrips`, rather than a census of
physical trains or standalone VehiclePosition entities. Return every route-attributable
record in that population, including future and canceled records as explicit outcomes;
only current, projectable records receive coordinates. Vehicles with no TripUpdate
are not presently retained as trains. Extending that population is a separate input
contract if required; this plan does not claim completeness over those raw entities.

No live A-line shape-quality or projection-success rate is established by the
synthetic [offline fixture](test/fixtures/reconcile/README.md). Those remain data
validation work, not assumed guarantees.

## Ownership and consistency

```text
scheduleingest -> immutable schedule version (shapes + patterns + stop anchors)
                                    |
ingest -> normalized realtime -> reconcile.Bundle
                                    |
                    projection.Route(bundle, routeID, now)
                                    |
                          httpapi JSON / GeoJSON
```

- `internal/scheduleingest`: bounded CSV decoding, optional-field presence, parser
  versioning, and the existing archive cache adapter.
- `internal/schedule`: immutable shape points, cumulative geometric distances,
  service patterns, and stop-occurrence anchors. Build these with the dataset;
  clone mutable inputs and returned collections as current lookups do.
- `internal/reconcile`: identity, occurrence resolution, route attribution, and
  an explicit decision authorizing or denying simulation at lookup time.
- New `internal/projection`: transport-neutral selection and interpolation over a
  bundle and supplied clock. Reuse existing basis, direction, and movement enums.
- `internal/httpapi`: request validation, endpoint selection, DTOs, UTC timestamp
  formatting, and GeoJSON coordinate ordering.
- `cmd/api`: pass the coordinator's result store into the new HTTP paths. Preserve
  the existing serialized publication callbacks and independent polling/startup.

Each new request captures one bundle and one `now`. Geometry and projection use
that bundle's schedule, never a separately loaded latest schedule or realtime
store. Freshness is recalculated from captured timestamps at lookup; saved
`Match.Train.Freshness` and `Metadata.ScheduleEligible` alone are insufficient.
Share the existing observation-freshness calculation instead of copying its rules.

There is no projection store, projection polling loop, database, or new service.
The current archive cache is the only persistence boundary. Request work performs
no upstream I/O. A newly published feed may precede its reconciliation callback;
new APIs serve the last complete bundle and expose its evaluation/source times.

## Confirmed decisions

| Decision | Why | Tradeoff | Alternative rejected or deferred |
| --- | --- | --- | --- |
| Add `view=positions` to the train endpoint; omission retains the compact view. | Full record coverage and simulation change list membership and `trip_id` semantics. | The initial map URL gains a parameter. | Changing the default list would break the stated compatibility requirement. A separate positions endpoint is viable but adds no capability here. |
| Add `Bundle.SimulationDecision(instance, now)` with an explicit `Allowed` result. Preserve `Fallback`'s existing contract. | Existing eligibility cannot safely authorize a coordinate. | Two named decisions with different purposes; share authority checks internally. | Checking `Eligible` or parsing reason strings in HTTP is unsafe. |
| Permit a stale, uniquely matched run to simulate under the same run ID using retained complete evidence. Deny missing, incomplete, or uncertain authority. | Staleness must remain visible without discarding a trustworthy scheduled identity. | Simulation ends at scheduled arrival or schedule expiry. | This confirmed decision supersedes the draft policy that disabled all outage simulation. |
| Use static trip ID plus service date as run identity; keep record identity separate. | Ambiguous and unmatched records cannot honestly claim a scheduled identity. | Some outcomes have no trackable run ID. | Nearest-position joins and physical-vehicle identity are excluded. |
| Derive geometry once per schedule version and calculate positions per request. | Reuses atomic ownership while allowing time to advance between feed updates. | Bounded route scans initially. | Independent caches and workers wait for measured latency or memory pressure. |

The API extension, null semantics, run identity, and simulation policy are the
expensive-to-reverse contracts. Review them in the implementation change before
making a browser depend on them.

## Geometry model and association

Retain `ShapePoint{sequence, latitude, longitude, optional feed_distance}` under
each `shape_id`. Also retain optional `stop_times.shape_dist_traveled`. Sort by
sequence, reject duplicate keys, non-finite/out-of-range coordinates, negative
distances, and invalid supplied distance order. Keep existing ZIP/CSV bounds.
Consecutive identical coordinates contribute zero geometric length; a shape with
fewer than two distinct points cannot provide a line or a moving position.

Calculate cumulative distances in meters from the coordinates. GTFS feed distance
units are not necessarily meters: when both tables supply usable distances, map
the stop's feed distance through the shape's distance axis to the geometric axis.
GTFS explicitly uses shared units across these tables and recommends distances
to disambiguate loops. [GTFS shape and stop-distance definitions](https://gtfs.org/documentation/schedule/reference/#shapestxt)

Without usable feed distances, project platform coordinates onto the trip's own
polyline, constrained by stop order and already known anchors. Require a unique,
nearby, nondecreasing assignment; never select a shape by proximity. Coalesce
equivalent candidates at adjacent segment endpoints. Repeated alignment visits,
ties at different distances, or conflicting order remain unresolved rather than
silently choosing an occurrence. Supplied contradictory distances must not be
replaced by a convenient nearest-point result.

Proposed initial association policy: a maximum 100 m platform-to-shape offset,
with candidates within 1 m of the best offset treated as tied if they identify
different along-shape positions. These are testable implementation defaults,
not measured MTA accuracy; verify them against available A-line data before
claiming operational coverage. Keep them as named internal constants initially.

Store anchor outcome/reason per scheduled occurrence, with segment index,
segment fraction, and along-shape meters when resolved. A missing shape ID/file,
dangling trip shape reference, or failed spatial association makes the affected
pattern unprojectable; it does not discard otherwise usable schedule data.
Malformed rows in a present shape table reject the refresh and retain the prior
version, following the existing decoder's failure policy.

Group patterns deterministically by route, direction ID, shape ID, ordered
platform-stop occurrences, and their distance associations; exclude trip times.
Use a content-derived `service_pattern_id`, not map iteration order. Retain the
trip-specific stop sequence/index mapping even when geometry is shared.
Patterns distinguish express/local service, branches, reverse directions, and
repeated stops. Do not invent express/local labels from an ID's spelling.

Canonical station IDs use `parent_station` when present, otherwise the stop ID.
Keep platforms and occurrence anchors distinct from station markers. Shared
stations keep the same IDs across route responses. Geometry can be stored once
by shape ID and referenced by multiple routes; do not introduce a shared-track
graph or merge distinct polylines geometrically.

## Selection, projection, and time

Start from every bundle match before applying route selection. Include records
whose explicit realtime route is selected, plus records attributable through a
unique scheduled match. For unresolved records, expose route candidates derived
by reconciliation; include an outcome in each implicated selected route without
assigning a scheduled identity. A record with no route evidence cannot be labeled
A merely because it came from the ACE feed; only supported route attributions
are included.

Preserve one outcome per source record, including duplicates. Do not call
`Train.Active`, collapse duplicates by static trip ID, or filter by successful
position calculation. Canceled/deleted retained records receive null coordinates
and a cancellation reason. Future or ended realtime records remain present with
`not_started` or `outside_prediction_window` when their timing establishes that
state. A missing timing window yields `insufficient_timing`, not a guessed lifecycle.

For each uniquely reconciled, non-canceled record:

1. Recheck schedule eligibility, source completeness/ownership, and feed age.
   Stale feed or trip-update evidence prevents realtime projection and may enter
   explicitly authorized schedule simulation under the same identity. Invalid
   supplied timestamps cannot authorize simulation. If that optional
   timestamp is absent, allow a fresh header to bound prediction age, expose
   `observation_basis: feed_header`, and leave observation freshness
   `unavailable`. Preserve timestamp presence in normalization so a malformed
   supplied timestamp cannot take this absence path.
2. Resolve the current/next stop to a scheduled occurrence. Retain the matched
   vehicle's optional stop ID, sequence, and status in normalized state. Use them
   only with fresh, unique vehicle association and compatible occurrence evidence.
   Otherwise use resolved stop updates. Do not substitute the first compact stop
   for an independently reported vehicle location.
3. Select the trip's shape and the relevant resolved occurrence anchors. An
   unresolved relevant stop, conflicting vehicle/update evidence, or unsupported
   path modification produces a null reason. Explicit `SKIPPED` stops are not
   dwell anchors; `NO_DATA` blocks prediction propagation across that boundary.
4. Prefer two compatible realtime event anchors bracketing `now`. Absolute event
   time wins; a supplied event delay can produce an anchor from the matching
   static event. Trip delay applies only where GTFS propagation permits it;
   do not invent zero delay or fill `NO_DATA`. Record how each anchor was derived.
   [GTFS realtime event and delay semantics](https://gtfs.org/documentation/realtime/reference/#message-stoptimeevent)
5. Support the common case with only a next-stop prediction. Estimate the prior
   departure by subtracting that scheduled segment's travel duration from the
   next predicted arrival. Require valid scheduled departure/arrival times,
   resolved endpoints, no contradictory/skipped/unknown intervening segment,
   and `now` inside the resulting interval. Expose
   `timing_method: next_prediction_schedule_duration`. This remains a
   realtime-anchored estimate, not an independently observed departure. MTA's
   reference describes past stops being removed after departure, so requiring
   two retained realtime events alone would miss this case.
   [MTA subway realtime reference, TripUpdate](https://www.mta.info/document/134521)
6. For a travel interval `[t0,t1]`, calculate `u = (now-t0)/(t1-t0)` and
   `distance = d0 + u*(d1-d0)`, then interpolate on the polyline at that distance.
   Require `t1 > t0` and `now` within the interval; clamp only numerical roundoff.
   Do not extrapolate or pin an overdue train indefinitely to a station.
   Use half-open adjacent intervals to make exact boundary selection deterministic.

A fresh matched `STOPPED_AT` observation at a resolved occurrence can anchor a
position at that shape distance; label its timing method `vehicle_stop_anchor`.
Realtime arrival/departure pairs can also hold a position during predicted dwell.
Neither case is GPS observation. Retain `reported_stopped` only for the fresh
vehicle report. Preserve the existing rule that movement is unknown without
usable vehicle evidence; simulated travel or dwell is not reported movement.
No new `gps_observed` basis is introduced.

An active realtime record with insufficient data stays null; it must not be
converted to schedule fallback to improve the number of markers.

## Schedule simulation authorization

Enumerate only the selected route's trips on `CandidateServiceDates(now)`, check
`Active(serviceID, date)`, and select runs within their scheduled first-departure
to final-arrival interval. Use the existing GTFS service-day conversion, including
extended hours and DST. A retained delayed realtime run is not discarded merely
because it is beyond its static arrival time.

`SimulationDecision` requires an eligible captured schedule, active service date,
exactly one configured owner, and a previously loaded complete snapshot with
usable metadata. Known cancellations, ambiguous/invalid candidates, and uncertain
source or route associations remain vetoes after the feed becomes stale. Fresh
runs with insufficient geometry or timing stay null; projection failure alone
cannot authorize simulation.

A stale uniquely matched run counts as present and may switch to simulation
under its existing run ID while retaining stale realtime provenance. Applicable
replacement windows do not veto that present run merely through membership.
For absent runs, replacement membership remains a veto after departure and after
freshness expiry. Retained windows never advance or extend with the clock.
The existing `Fallback` contract still uses fresh, future-departure suppression;
the separate rendering decision reuses its evidence and interval logic.

Simulation may continue while schedule eligibility permits it, defaulting to 24
hours after successful schedule validation. No additional outage timer is added.
Only scheduled first departure through final arrival is simulated; stale delayed
runs beyond that interval remain null. No cancellation ledger or track history
is introduced: decisions use the captured complete snapshot.

Interpolate permitted runs between scheduled event/shape anchors using the same
geometry calculation. Missing intermediate timepoints may be spanned by valid
scheduled anchors as a declared linear estimate; no value is written back into
the schedule. Use `position_basis: schedule_fallback`, `movement_state: unknown`,
and `interpolation_method: schedule`. If geometry still fails, retain the
authorized active fallback outcome with a null reason. Denied schedule candidates
do not become train records; retained realtime outcomes include their denial reason.

## Public contract and browser identity

| Interface | Proposed behavior |
| --- | --- |
| `GET /api/v1/routes/{route_id}/geometry` | Route metadata, schedule version/eligibility, canonical station Points, shape LineStrings, and patterns containing ordered platform/station occurrences and anchor outcomes. GeoJSON uses `[longitude, latitude]`. Ordering belongs to each pattern; a branching route has no single station order. |
| `GET /api/v1/trains?route_id=A` | Existing compact selection, DTO, source errors, sorting, and freshness behavior. |
| `GET /api/v1/trains?route_id=A&view=positions` | All attributable realtime outcomes plus authorized active schedule outcomes from one bundle. Unknown explicit views return `400 invalid_view`. |
| `GET /api/v1/stations?query=...` | Deferred: the geometry response supplies the canonical stations needed by this map. Add search when a station selector requires it. |

The positions envelope includes `projected_at`, `bundle_evaluated_at`, nullable
`schedule_version`, current schedule eligibility, source coverage/timestamps,
`trains`, and independent source/schedule ages. These timestamps distinguish computation time from
data age. Coverage describes loaded sources, not projection success.

Each train outcome contains:

| Fields | Semantics |
| --- | --- |
| `source_record_id` | Distinguishes each realtime source record using source/entity/index provenance; it is nullable for scheduled outcomes and is not an animation key. |
| `route_id` | Selected route grouping. Conflicting route evidence cannot authorize a position. |
| `trip_id`, `service_date`, `trip_instance_id` | Static scheduled identity when uniquely established; otherwise nullable. Preserve the original realtime descriptor in `realtime_trip_id` and `realtime_service_date`. |
| `direction_id`, `direction` | Static 0/1 direction separately from supplied compass direction; unknown stays explicit. |
| `shape_id`, `service_pattern_id`, `schedule_version` | Identify the actual versioned geometry used, nullable when unavailable. |
| `latitude`, `longitude`, `projected_at` | Coordinates are both present or both null; timestamp is the evaluation instant, not an observation. |
| `position_basis`, `interpolation_method` | Only `realtime_prediction` or `schedule_fallback`. For null records the basis describes the attempted path, not successful localization. Timing method identifies additional schedule-derived estimates. |
| `movement_state`, `freshness`, `observation_basis` | Preserve feed freshness independently from projection success and observation age. Simulation is visibly labeled even with a fresh source. |
| `source_name`, `source_timestamp`, `received_at`, `entity_id` | Realtime input provenance; for fallback, identify the authority source separately from static schedule metadata. |
| Trip/vehicle observation timestamps, freshness, vehicle entity/match | Reuse existing observation fields without promoting missing observations to fresh. |
| `schedule_source`, `schedule_validated_at`, `fallback_reason` | Static source/age and the decision supporting simulation. |
| `no_position_reason`, `reconciliation_outcome`, `reconciliation_reason` | Explicit failure code when coordinates are null, with the original reconciliation explanation retained. |

Use deterministic, unambiguous encoding of `[static_trip_id, service_date]` for
`trip_instance_id`; never include poll time, feed entity, schedule version, or
position. For example, base64url of the JSON tuple is sufficient. Sort by route,
run identity, then record identity, without deduplicating uncertain records.

The browser uses that run ID across polls. It stops interpolation on a null or
missing outcome, ambiguity, changed realtime trip ID, changed service pattern,
changed geometry version/direction, or a basis transition. Resume as a new visual
segment after such a break; do not merge uncertain identities. The stable run ID
can remain the same while an animation segment restarts. No server track history
or physical-railcar identity is required, and continuity between unobserved poll
states is not promised.

Bounded null reasons include `canceled`, `unmatched_trip`, `ambiguous_trip`,
`invalid_trip`, `schedule_missing`, `schedule_ineligible`, `stale_source`,
`stale_trip_update`, `invalid_trip_timestamp`, `source_incomplete`,
`route_conflict`, `missing_shape`, `unusable_shape`, `unresolved_stop`,
`ambiguous_shape_anchor`, `stop_off_shape`, `no_prediction_data`,
`insufficient_timing`, `invalid_timing`, `conflicting_stop_evidence`,
`unsupported_service_pattern`, `not_started`, and `outside_prediction_window`.
Define deterministic precedence in tests: cancellation/identity/authority errors
precede geometry and timing errors. Never use `(0,0)` for missing coordinates.

## Failure, deployment, and evolution

- New geometry requests return `503 schedule_unavailable` when there is no
  captured schedule, and `404 route_not_found` when a loaded schedule lacks the
  route. Retained ineligible geometry remains inspectable with explicit schedule
  state; missing shapes produce partial geometry with per-pattern reasons.
- A captured realtime source without a schedule returns `200` positions with null
  realtime outcomes. A stale source likewise returns `200`, with simulations
  only when the explicit reconciliation decision permits them. A relevant source that has never loaded, or an
  absent initial bundle, returns a structured `503`, not a misleading empty list.
  Preserve existing configured/observed-route source ownership semantics.
- Geometry and positions are separate requests: both carry `schedule_version`.
  A browser detecting mismatch discards older responses, refetches geometry, and
  restarts interpolation. No cross-request atomicity or historical version-serving
  endpoint is claimed.
- Increment `ParserSchema` for the shape-aware dataset. The current restore path
  rejects old parser schemas, so deployment requires a successful new schedule
  fetch before geometry is available if only an old cache exists. Do not renew
  validation time by reinterpreting cache bytes. Automatic schema migration is
  deferred unless offline upgrade recovery becomes a requirement; document this
  rollout limitation. Cache write failure still permits in-memory publication.
- Keep current health/readiness, compact active-count metrics, source polling,
  safe error logs, non-root container, and timeouts. Add bounded geometry/outcome
  counts and duration diagnostics using existing telemetry patterns; never use
  trip IDs as metric labels. Retain detailed per-record reasons in responses.
- Treat route IDs as opaque lookup keys, never paths or upstream URLs. Keep
  numeric validation and archive limits at ingestion boundaries. New endpoints
  are read-only and introduce no credentials, writes, or external calls.

Ponytail lite favors the existing immutable dataset and standard-library math.
The simpler delivery also omits station search because embedded station records
already satisfy the map requirement.

Defer spatial indexes or a projection response cache until profiling shows route
scans/encoding exceed the browser polling latency budget; then index by route and
cache immutable geometry by version. Revisit alignment/interpolation only when
validated samples show ambiguous anchors or visible errors from the simple model.
Add a multi-route batch only when measured request cost or a required shared
evaluation instant makes separate requests inadequate. Add push only when polling
misses a defined update-latency target. Historical trails, nearest-position joins,
physical-car identity, journey filtering, all-route rollout, and the browser remain
outside this item; each needs its own requested feature and acceptance criteria.

## Delivery phases and verification

1. **A-line geometry through HTTP.** Parse shapes/distances, build immutable
   patterns and anchors, wire bundle-backed geometry, and document the endpoint.
   Acceptance: direction/branch/local/express fixtures return valid LineStrings
   and correctly ordered station occurrences; missing geometry is explicit;
   another synthetic route uses the same implementation. Test malformed rows,
   non-finite values, sequences, distance units, repeated stops, loops/ties,
   zero-length segments, copying, schedule replacement, and cache schema/restore
   behavior. Upgrade the existing dummy shape fixture to valid shape data.
2. **Complete realtime outcomes through the opt-in view.** Preserve required
   vehicle/timestamp evidence, retain all attributable bundle matches, and add
   projection/identity/provenance DTOs and interpolation. Acceptance: one output
   per input record, including sequence-only, missing-trip-ID, empty-stop,
   ambiguous, canceled, stale, and unprojectable records. Verify two-anchor,
   next-prediction, delay-only, stopped, dwell, skipped, `NO_DATA`, unknown/future
   observation, and invalid-time cases. Assert a known coordinate on a bent
   polyline, so interpolation across the direct station-to-station chord fails.
   Repeated polls advance a position without changing its scheduled run ID.
3. **Authorized simulation through the same view.** Add the reconciliation
   rendering decision and active scheduled enumeration. Acceptance: an eligible
   absent run outside replacement authority projects with `schedule_fallback`;
   no active counterpart produces a duplicate. Verify cancellation outside a
   replacement window, ambiguous/invalid/unidentified counterparts, duplicate
   owners, malformed windows, start-inclusive/end-exclusive boundaries,
   suppression across departure, source aging without publication, schedule
   expiry, overnight service, DST, and missing geometry. Assert fresh-to-stale-to-
   fresh transitions preserve one run ID and change basis without duplicates;
   uncertain authority remains a veto and the old `Fallback` contract stays tested.
4. **Verify the complete API slice and compatibility.** Extend the offline ZIP/
   protobuf fixture through both HTTP responses with known geometry and expected
   outcomes. Concurrent schedule/feed publication must never mix geometry and
   match versions; separate polls preserve identity and expose version changes.
   Confirm HTTP startup while schedule fetch blocks, retained data after failures,
   missing-source errors, stale lookup transitions, OpenAPI examples, and unchanged
   compact selection/readiness/metrics. Extend the existing container smoke check
   for unavailable new endpoints; use offline Go integration tests for successful
   map data rather than requiring live MTA access.

Implementation completion requires `go test ./...`, `go test -race ./...`,
`go vet ./...`, clean `gofmt -l` output for changed Go files, and
`./test/container_smoke.sh`. There is no separate configured linter or additional
type checker in the inspected project; Go compilation and vet cover those checks.
Run the optional local-MTA-archive test when an archive is available, and inspect
anchor outcomes before asserting real A-line coverage. Do not count a synthetic
fixture as a measured live projection rate.

The plan covers the requested backend acceptance criteria through these four
phases. Material limits remain explicit: the retained TripUpdate population,
unmeasured A-line anchor quality, explicitly labeled outage simulation, the opt-in API
extension, and parser-schema rollout recovery.

Planning verification: existing tests passed for `internal/reconcile`,
`internal/schedule`, `internal/scheduleingest`, `internal/httpapi`, and
`integration`. The integration suite required localhost socket access beyond
the sandbox; Go/ccache settings were adjusted for the read-only home cache.
Markdown whitespace checks passed. No application code changed, and the existing
`ARCHITECTURE_PLAN.md` worktree edit was preserved. Race, vet, and container checks
remain implementation gates; this baseline does not certify the planned feature.
