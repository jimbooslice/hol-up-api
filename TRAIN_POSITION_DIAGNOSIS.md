# Why A-line train markers are missing

Investigation: 2026-09-15. The original observations and experimental replays below
describe the backend before the fix. The implementation and verified results are
recorded in the final section.

## Findings

The largest blocker is the treatment of vehicle stop-sequence disagreement as a
veto on all positioning, including otherwise usable trip predictions. The prior
follow-up deliberately kept vehicle associations strict, so it did not address
this source of live failures.

The timing category also needs a clearer lifecycle label: every baseline
`insufficient_timing` record in these two captures is still at its first
scheduled occurrence with a future predicted departure. It does not represent
an active train that needs to be moved along the line by schedule simulation.

## Capture and replay scope

| Capture | ACE header time (UTC) | Receipt/evaluation time (UTC) | A records | Positioned | Vehicle stop conflicts | Insufficient timing | Other failures |
| --- | --- | --- | --- | --- | --- | --- | --- |
| First | 2026-09-15T21:10:10Z | 2026-09-15T21:10:21Z | 52 | 2 | 32 | 16 | 2 |
| Second | 2026-09-15T21:15:11Z | 2026-09-15T21:15:16Z | 53 | 0 | 32 | 19 | 2 |

Both replays use the complete ACE snapshot (all A/C/E/H records enter
reconciliation before route A is selected), one fixed evaluation time per
capture, and this captured supplemented schedule:

- Version: `supplemented:2:c2b19bdbe33f064f9c1af38f3b27a210291db6de16fc080e48321595772c3613`.
- Validated: `2026-09-15T12:45:02.097838209-04:00`.
- It remained eligible at both evaluation times. The first replay uses a schedule
  validated approximately 4.42 hours earlier.
- Other live feed groups were not part of these replays. Counts describe these
  captured inputs, rather than a simultaneously running eight-feed HTTP server.

The current supplemented download returned HTTP 200 and 1,154,957 bytes on two
attempts, but both downloads lacked a ZIP end-of-central-directory record and
failed ZIP validation. The known-good cached ZIP contains 20,687,864 bytes. This
limits comparison with the latest schedule; it does not explain the replay's
null positions, since the captured cached schedule is loaded and eligible.
The agency's internal reason for sequence discrepancies is not established.

## What is present and absent

For the first capture's 52 A-line records:

| Evidence | Observed payload |
| --- | --- |
| Trip ID, route, service date, start time | Present; 51 records match scheduled instances, one does not |
| Stop predictions | 1,179 updates with stop IDs and absolute arrival/departure times |
| Stop-update sequences | Absent on all 1,179 updates |
| Separate arrival/departure intervals | All 1,179 arrival/departure timestamps are equal; these alone supply no positive dwell duration |
| Vehicle entities | Present for all 52 records; 32 have conflicting ID/sequence pairs |
| Vehicle latitude/longitude | No `position` object in any of the 52 vehicle entities |
| Trip-update timestamps | Absent on all 52; current code uses feed-header freshness |
| Trip-level delay | Absent on all 52 |
| Prediction uncertainty | Absent on all arrival/departure events |
| NYCT `is_assigned` | Explicitly true on 32 records; absent on 20. Absence is not an explicit false value |

The old MTA reference says passed stops are dropped and the first retained stop
is the stop being approached, served, or left. It also documents transit-time
predictions and omits vehicle coordinates. Its original scope is the A Division
and its date is 2012; the counts above establish what this current ACE capture
actually contains. [MTA reference, pages 4, 8–12](https://www.mta.info/document/134521).

The ingestion adapter preserves the supplied vehicle sequence without adding or
subtracting one. These discrepancies already exist between the raw feed and the
captured schedule; they are not introduced by a local sequence conversion.
See `internal/ingest/ingest.go` and `internal/reconcile/match.go`.

## Example 1: usable train rejected by vehicle numbering

Trip: `095900_A..N55R`, uniquely matched to
`L0S2-A-1087-S01_095900_A..N55R` on service date `20260915`.

Relevant raw vehicle fields (other fields omitted):

```json
{
  "current_stop_sequence": 25,
  "stop_id": "A31N",
  "current_status": "STOPPED_AT",
  "timestamp": "1789506592"
}
```

The trip update independently identifies that same stop:

```json
{
  "stop_id": "A31N",
  "arrival": { "time": "1789506592" },
  "departure": { "time": "1789506592" }
}
```

The captured schedule contains:

```csv
stop_sequence,stop_id,stop_name
25,A32N,W 4 St-Wash Sq
26,A31N,14 St
27,A28N,34 St-Penn Station
```

The vehicle ID and trip update agree on 14 St. But `current_stop_sequence: 25`
selects W 4 St from the schedule. Strict `ResolveStop` returns
`stop_id_sequence_conflict`. `PositionEvidenceReason` then rejects the whole
train before the projector tries its usable timing evidence. This vehicle
observation is only 29 seconds old, so this example is not caused by expiration.

The same trip's timing, converted to New York local time for readability:

```text
Evaluation:                17:10:21
14 St prediction:          17:09:52
34 St-Penn prediction:     17:11:53
```

Those predictions bracket evaluation time. The existing realtime interpolation
can place an inferred train along that segment if the unusable vehicle
observation is excluded. This estimate is not a measured GPS position.

### Why adding one is insufficient

All 32 conflicting vehicle IDs occur exactly once in their matched trips. The
supplied sequence falls below the scheduled sequence for that ID by:

| Difference | Records in each capture |
| --- | --- |
| 1 | 26 |
| 2 | 4 |
| 3 | 2 |

Examples beyond one:

- `096150_A..S87R`: vehicle `H02S`, sequence 22; the schedule places `H02S`
  (Aqueduct-N Conduit Av) at sequence 24.
- `100150_A..N54R`: vehicle `A41N`, sequence 10; the schedule places `A41N`
  (Jay St-MetroTech) at sequence 13.

The majority is consistent with zero-based versus one-based counting, but the
larger discrepancies show that this alone is not a sufficient explanation or
safe universal repair. Different stopping patterns or schedule revisions are
plausible contributors; the captures do not prove the publisher's cause.
The GTFS reference defines the vehicle's stop ID and sequence as identifying its
current stop in conjunction with status, so disagreement must be handled
explicitly. [GTFS VehiclePosition reference](https://gtfs.org/documentation/realtime/reference/#message-vehicleposition).

Nine of the 32 conflicting vehicle observations in the first capture are already
stale; eleven are stale in the second. Current code still allows their unresolved
association to veto fresh trip predictions. Also, only 18 conflicting vehicle IDs
in the first capture agree with the *first* remaining prediction: old vehicle
observations can lag the rolling prediction list. A corroboration rule must
account for observation time rather than assume these always agree.

## Example 2: upcoming service described as insufficient timing

At evaluation time **17:10:21**, trip `103650_A..S78R` contains:

```json
{
  "trip_id": "103650_A..S78R",
  "start_time": "17:16:30",
  "start_date": "20260915"
}
```

Its first prediction is:

```json
{
  "stop_id": "A03S",
  "arrival": { "time": "1789506990" },
  "departure": { "time": "1789506990" }
}
```

`1789506990` is **17:16:30 EDT**, 6 minutes 9 seconds after evaluation.
`A03S` is Dyckman St, the first stop of this scheduled trip. There is no earlier
segment in this run to interpolate. Its vehicle entity omits both current status
and sequence, and its timestamp is also that future departure time; it cannot
establish a current stopped position.

All 16 baseline timing failures in the first capture, and all 19 in the second,
have a future first-stop prediction. Label these `upcoming` / `not_started` and
show their departure times. Keep them in the list, with a separate count from
currently positioned trains. A terminal preview could be an explicit UI feature,
but it should not claim a current train location or premature movement.

After ignoring conflicting vehicle observations in the first replay, one more
record becomes `insufficient_timing`: `093200_A..N55R` has no remaining stop
updates and a stale terminal vehicle observation. Its present location remains
unsupported. That is a distinct lifecycle/evidence case from upcoming service.

## Other failures that should stay explicit

- `103625_A..N54R` has no exact/suffix scheduled candidate. Do not silently pick
  the nearest departure time and assign another scheduled run's identity.
- `106600_A..S51R` matches a scheduled trip ending at Euclid Av (`A55S`), but
  realtime supplies additional stops continuing toward Lefferts Blvd. Its
  `unknown_stop` means outside the matched trip, not necessarily an unknown
  station globally. This is stronger evidence of a changed stopping pattern.
  Refresh/compare valid schedules and reconcile the path before drawing it.
- Some replay variants expose `ambiguous_stop_anchor`, previously hidden behind
  the vehicle veto. Geometry must still identify a safe location on the shape.

## Resolution options, measured on identical captured inputs

These experiments alter cloned normalized evidence solely for replay. They do
not implement warning propagation, a final policy, or new simulation permission.
Every option still uses the existing identity, timing and geometry code.

| Experimental rule | First capture positioned / 52 | Second capture positioned / 53 | Tradeoff |
| --- | --- | --- | --- |
| Original behavior | 2 | 0 | One bad vehicle association blocks the whole train |
| Exclude only conflicting vehicle observations; use independent trip predictions | 31 | 31 | Retains strict rejection of the vehicle observation; loses its stopped-state evidence |
| Prefer a unique vehicle stop ID when it agrees with the first trip prediction | 19 | 14 | Stronger corroboration, but rolling predictions often advance past the reported vehicle stop |
| Prefer unique vehicle stop ID over its conflicting sequence | 32 | 32 | Broader tolerance; can retain reported-stop positioning when fresh, but needs clear acceptance rules and warnings |

The ID-preference experiments omit the conflicting sequence in a clone so the
existing unique-ID resolver can be measured. A production implementation should
retain raw identifiers and record a corrected association plus warning instead.
The broad experiment's additional marker is not proof of a more accurate policy.

Original implementation recommendations (items 1–2 are now implemented):

1. Separate rejected vehicle evidence from otherwise valid trip predictions.
   For a sequence-only disagreement with a uniquely identified stop and run,
   exclude the unusable vehicle observation from realtime positioning and emit
   a specific warning. Preserve conflicting stop IDs/paths, ambiguity, unknown
   occurrences, invalid timing, cancellation and geometry checks. Do not expand
   stale simulation permissions as a side effect.
2. Add an explicit upcoming-trip state/reason before the generic timing failure.
   Report active/positioned and upcoming counts separately. Preserve the
   distinction between omitted assignment and explicit false.
3. Consider corroborated vehicle-ID preference after the independent-prediction
   path is verified. Require unique occurrence, compatible ordered updates and
   appropriate observation freshness; preserve the original ID and sequence.
4. Diagnose actual path changes and ambiguous shape anchors independently.
   Browser animation can follow once positions are accepted.

The simpler initial fix is to reject the bad optional observation and use the
valid predictions already available. These captures do not support adding broad
schedule simulation merely to make every feed record appear as an active train.

## Local evidence

Full raw protobuf, decoded raw JSON and replay details were saved locally:

- `/tmp/holup-blockers/ace.pb`, `feed.json`, `diagnosis.json`.
- `/tmp/holup-blockers-second/ace.pb`, `feed.json`, `diagnosis.json`.
- `/tmp/holup-blockers/cached-schedule.zip` is the exact schedule used.
- `/tmp/holup-blockers/schedule.zip` and `schedule.headers` retain the invalid
  latest download and response metadata.

Raw examples above were checked against the decoded protobuf and scheduled
occurrences. The original experimental replay files remain unchanged.

## Implemented policy and verification

Fresh trip predictions can now position a train after excluding a vehicle stop
association rejected as `stop_id_sequence_conflict`, provided the vehicle stop ID
occurs exactly once in its uniquely matched trip. The API reports
`vehicle_stop_sequence_conflict_ignored`. It does not correct the vehicle ID or
sequence, use the rejected stopped-state observation, or allow it to constrain
the interpolated segment. Raw evidence remains intact, and the conflict still
vetoes stale schedule simulation. Other identity, timing, path and geometry
failures retain their checks.

A validated future departure at the first scheduled occurrence now returns
`no_position_reason: "not_started"` and `predicted_departure_at`, with null
coordinates and unknown movement. Current predicted dwell or accepted fresh
stopped-state evidence prevents this classification. Upcoming classification
precedes geometry lookup and uses no NYCT assignment flag: omission is not
interpreted as explicit false. The app shows the departure in New York time and
counts positioned, upcoming and unavailable records separately.

Both complete ACE captures were replayed through the implemented policy, without
editing the normalized evidence or original capture files:

| Capture | Positioned | Upcoming | Other unpositioned outcomes | Vehicle exclusion warnings |
| --- | --- | --- | --- | --- |
| First (52 records) | 31 | 16 | 2 ambiguous anchors, 1 insufficient timing, 1 unmatched identity, 1 unknown stop | 32 |
| Second (53 records) | 31 | 19 | 1 insufficient timing, 1 unmatched identity, 1 unknown stop | 32 |

These are captured-input results, not a guarantee about the changing live feed.
The remaining insufficient-timing records are not classified as upcoming.
Vehicle-ID preference, changed-path repair and browser animation remain separate.

To repeat a replay, use either capture directory with the same cached schedule:

```sh
HOLUP_TEST_POSITION_CAPTURE=/tmp/holup-blockers \
HOLUP_TEST_SCHEDULE_ARCHIVE=/tmp/holup-blockers/cached-schedule.zip \
go test ./integration -run TestLocalPositionCapture -v
```

The optional replay test skips without these local files configured; synthetic
conflict, lifecycle, HTTP and browser regressions run in the normal suites.

Verification: `go test -race ./...`, `go vet ./...`, all 39 web unit tests,
TypeScript checking, and the production build passed. The browser run passed
38 of 39 tests, including the new upcoming/marker transition test. The remaining
manual-retry test raced automatic geometry recovery; its fixture and the sibling
timeout-retry fixture now use a null positions schedule version to isolate manual
retry. Both passed three consecutive runs afterward (6/6). Application polling
behavior was unchanged.
