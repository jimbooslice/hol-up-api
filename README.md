# Hol Up transit API

This repository polls all eight anonymous feeds in MTA's Subway Realtime
catalog, decodes the standard and NYCT GTFS-Realtime schemas, and atomically
serves each feed independently. This covers every subway route group and the
Staten Island Railway feed that MTA includes in that catalog; it does not ingest
bus, LIRR, or Metro-North feeds.

## Run

```sh
go run -buildvcs=false ./cmd/api
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl 'http://localhost:8080/api/v1/trains?route_id=A'
```

`ADDR` overrides the listen address. Feed URLs can be overridden independently:

| Source | Configured route IDs | Environment variable |
| --- | --- | --- |
| `ace` | A, C, E, H | `MTA_GTFS_RT_URL` |
| `bdfm` | B, D, F, FX, M, FS | `MTA_GTFS_RT_BDFM_URL` |
| `g` | G | `MTA_GTFS_RT_G_URL` |
| `jz` | J, Z | `MTA_GTFS_RT_JZ_URL` |
| `l` | L | `MTA_GTFS_RT_L_URL` |
| `nqrw` | N, Q, R, W | `MTA_GTFS_RT_NQRW_URL` |
| `numbered` | 1, 2, 3, 4, 5, 6, 6X, 7, 7X, GS | `MTA_GTFS_RT_NUMBERED_URL` |
| `si` | SI, SS | `MTA_GTFS_RT_SI_URL` |

`GS`, `FS`, and `H` are the three shuttle route IDs. `SS` is an undocumented
SIR service ID observed in MTA's realtime feed and is assigned to the `si`
source; `SI` remains the canonical static route ID. A failed refresh never
replaces that source's last known good snapshot.

`MTA_GTFS_RT_MAX_FUTURE_SKEW` limits how far a feed header timestamp may be
ahead of local receipt time for all eight feeds. It is read once at startup
as a Go duration (for example, `30s` or `1m250ms`); unset or empty defaults to
`30s`, and `0` enables strict validation. Malformed or negative values stop
startup before polling or serving. The startup log records
`max_future_skew_seconds`.

A header more than this tolerance ahead is rejected as a whole refresh. The
exact boundary is accepted, and future arrival/departure predictions remain
valid. Rejected refreshes are discarded; normal polling retries every 30
seconds without rewriting timestamps, queuing payloads, clearing healthy
data, or restarting. Retained snapshots age into `stale` after one minute;
an initial rejection leaves that source `unavailable`. Other feeds continue
independently, and readiness still requires any one fresh feed.

The host wall clock is the reference and is expected to be synchronized.
Existing per-feed ordering remains enforced: an accepted timestamp within
the tolerance can temporarily block a slightly earlier update.

Successful route and train responses report `coverage` as `complete`,
`partial`, or `unavailable` and include per-source configured routes, source
timestamp, and freshness. `/api/v1/routes` returns routes from any source that
has loaded. Every configured route query depends only on its owning feed. An
additional route observed in an available feed remains queryable when another
feed is down; an unobserved route returns `503 source_unavailable` until all
feeds have loaded, so an empty result never implies missing trains. Stale
last-known-good data remains queryable.

`/readyz` returns `200` when at least one feed is fresh and reports complete or
partial source coverage separately. `/healthz` only reports process liveness.
Source-unavailable errors include the same coverage and source details as
successful responses.

Available endpoints are `GET /healthz`, `/readyz`, `/metrics`,
`/api/v1/routes`, `/api/v1/trains?route_id=A` (or another returned route), and
`/openapi.yaml`. Open `http://localhost:8080/openapi.yaml` in a browser or
pass it to an OpenAPI viewer for the complete API contract.

`/metrics` uses Prometheus's text exposition format. Alongside aggregate
readiness and train count, it reports bounded per-feed refresh outcomes,
durations, timestamps, freshness, and train counts with one of the eight source
names as the `feed` label.

## Realtime semantics

`predicted_at` and `prediction_delay_seconds` are independently nullable. An
absolute arrival/departure does not imply zero delay. Explicit event delay
wins over an explicit trip delay; omitted values remain unknown. The compact
view prefers an absolute arrival, then an absolute departure, then a delay-only
event. `NO_DATA` suppresses both prediction and delay, including trip-delay
fallback. No absolute time is invented from delay without a static schedule.

Train `freshness` continues to describe the source feed. The separate
`trip_update_timestamp` / `trip_update_freshness` and `vehicle_timestamp` /
`vehicle_freshness` describe upstream observations using the same one-minute
threshold (the exact boundary remains fresh). Missing or future observation
times have unavailable freshness. An old feed caps observation freshness at
stale. Movement is unknown without a fresh, uniquely matched vehicle observation;
a fresh feed alone cannot establish movement. Trip and vehicle age are independent.

`vehicle_match` is `matched`, `unmatched`, or `ambiguous`. Descriptors must agree
on every supplied trip ID, route, service date, start time, and direction, with
exactly one compatible record in each direction. Route filtering happens after
ambiguity detection. Duplicate descriptors are conservatively ambiguous.
`start_time` distinguishes repeated instances on the same service date.

Normalized state retains all trip updates with a trip ID, including cancellations,
deletions, trips with no stops, and stops identified only by sequence. Each stop
retains its relationship, optional sequence, and separate arrival/departure time,
delay, uncertainty, and scheduled time. Stops are sorted when all sequences are
present; otherwise their feed order is retained until static schedules can resolve
missing sequences. The compact train API skips `SKIPPED` stops, excludes canceled
or deleted trips and trips without an identified route or next served stop, and retains
served `NO_DATA` stops with null predictions. Active train metrics use the same
selection. Observed routes remain known even when every trip is canceled.

Each retained trip carries its feed name, header timestamp, local receipt time,
trip entity ID, and the matched vehicle entity ID and observation timestamp.
The API exposes these as `source_name`, `source_timestamp`, `received_at`,
`entity_id`, `vehicle_entity_id`, and the observation fields above. Full stop and
cancellation records stay in memory for later station and journey work; they are
replaced atomically with each valid full feed. Failed refreshes retain them.
Source names and receipt times are supplied by the loader, not inferred from URLs.

## Static schedules

Static subway schedules are also loaded independently at startup and hourly.
They remain internal; realtime endpoints and readiness retain their existing behavior.

| Setting | Default | Purpose |
| --- | --- | --- |
| `MTA_GTFS_SUPPLEMENTED_URL` | `https://rrgtfsfeeds.s3.amazonaws.com/gtfs_supplemented.zip` | Preferred schedule archive |
| `MTA_GTFS_REGULAR_URL` | `https://rrgtfsfeeds.s3.amazonaws.com/gtfs_subway.zip` | Fallback when no eligible supplemented data remains |
| `SCHEDULE_CACHE_DIR` | `/tmp/hol-up/schedule` | Directory containing `active.json`; mount a writable volume for restart recovery across container replacement |
| `SCHEDULE_MAX_AGE` | `24h` | Positive Go duration since successful upstream validation; invalid values stop startup |

Each hourly cycle tries supplemented GTFS first. A failed refresh retains the
current dataset. Eligible retained supplemented data takes precedence over
regular GTFS; regular is fetched only when supplemented is unavailable, expired,
or outside advertised coverage. A successful supplemented refresh replaces a
regular fallback. Expired versions remain inspectable but cannot support future
reconciliation. The exact maximum-age boundary is eligible; future validation
timestamps are ineligible. Service coverage also bounds eligibility, including
extended-hour trips from the final advertised service date; it never activates
an unadvertised service date.

`internal/schedule` owns service evaluation and immutable trip, route, service,
and ordered-stop lookups. `internal/scheduleingest` owns HTTP, ZIP/CSV decoding,
and the cache adapter. Every reader captures one version containing the dataset,
indexes, source kind, archive SHA-256, parser schema, freshness, and coverage.
Construction copies staging data; lookups return copies. Identical content keeps
its version while successful upstream validation renews freshness atomically.

All routes in the archive are ingested without aliases. CSV tables support
reordered/extra columns, UTF-8 BOMs, and exception-only calendars. Invalid keys,
references, sequences, required values, dates, and times reject the whole archive.
Intermediate optional times retain presence; no missing time is interpolated.
Times use nonnegative signed 32-bit seconds, including hours beyond 24:00, from
service-day local noon minus twelve elapsed hours. Exceptions override weekly
service, and candidate dates include previous overnight service days. New York
timezone data is embedded in the binary. See the [GTFS time definition](https://gtfs.org/documentation/schedule/reference/#field-types).

Base calendar coverage is intersected with any advertised `feed_info` date bounds.
Supplemented planning coverage is recorded separately as the local validation
date through six calendar days later, clipped to service coverage. This is an
explicit interpretation of MTA's [seven-calendar-day service-change horizon](https://www.mta.info/developers),
not a guarantee that every change is represented. Restoring a cache cannot advance
this horizon or its validation time.

Downloads have a 60-second timeout and a 64 MiB archive limit. ZIP validation
limits both declared and actual total output to 512 MiB, each entry to 256 MiB,
and the archive to 64 entries. Each raw logical CSV record, including quoted
multiline fields, is bounded to 1 MiB before CSV decoding. Files are read directly
from the archive; duplicate filenames, nested paths, malformed archives, checksum
failures (including unused files), and exceeded limits are rejected. Shape and
journey processing remain deferred.

The cache uses a small load/save interface and one JSON envelope containing
metadata and archive bytes. A private temporary file is synced and atomically
renamed to `active.json`, then the directory is synced. Startup bounds cache
reads, verifies the hash/schema and metadata, reparses the archive, and rebuilds
indexes. Interrupted temporary files do not affect the active cache. Cache-write
failure warns but does not block valid in-memory publication. Memory-only
retention is the smaller alternative; disk retention was selected for restart
recovery. Multiple processes must use separate cache directories.

`schedule_published` INFO logs include source, version, publication/validation
times, base/planning coverage, counts, duration, and selection reason. Failed
refreshes warn once per source outage and log recovery after successful upstream
publication. `schedule_retained` and `schedule_fallback` explain source selection;
cache rejection/write failures have separate warnings. Error summaries never
include upstream URLs, CSV values, response bodies, or filesystem paths.

Internal reconciliation now publishes immutable schedule/source bundles after
each successful realtime or schedule publication, including cache restoration
and unchanged-content renewal. It records exact/MTA identity matches, ambiguous
and invalid candidates, stop associations, and validated NYCT replacement rules.
Fallback lookups recheck freshness and eligibility; HTTP responses and readiness
retain their existing behavior. Public schedule APIs and fallback rendering are
deferred. See [reconciliation decisions](ARCHITECTURE_PLAN.md#internal-reconciliation-decisions).

`LOG_LEVEL` defaults to `INFO`; case-insensitive `DEBUG`, `INFO`, `WARN`, and
`ERROR` are accepted. Invalid values stop startup without echoing the value.
`reconciliation_completed` INFO events report the trigger, schedule eligibility,
source provenance, outcome counts, and duration. `reconciliation_trip` DEBUG
events explain identifiers, selected rules, candidates, and rejection reasons.
The offline [A-line ZIP/protobuf fixture](test/fixtures/reconcile/README.md)
exercises ingestion through reconciliation.

## Verify

```sh
go test ./...
go test -race ./...
go vet ./...
./test/container_smoke.sh
```

The container build runs the Go tests before producing a non-root runtime
image. The smoke script uses Docker or Podman, checks liveness, unavailable-data
responses, metrics and OpenAPI with unreachable upstreams, and verifies JSON
startup and SIGTERM shutdown events (requires curl and jq). The Go suite also
starts the application with a blocked schedule request and confirms realtime
and HTTP startup continue independently.

For supplementary verification against a locally downloaded MTA ZIP:

```sh
HOLUP_TEST_SCHEDULE_ARCHIVE=/path/to/gtfs_supplemented.zip go test ./internal/scheduleingest -run TestLocalMTAArchive -v
```

Offline fixtures are the acceptance gate; the real-archive check is optional.

Structured lifecycle and feed failure/recovery logs go to stdout. Error fields
contain safe summaries and retain only numeric upstream HTTP statuses. Unknown
failures intentionally lose their original detail. A future timestamp produces
a `realtime_feed_refresh_failed` WARN with a safe summary,
`reason="future_source_timestamp"`, numeric Unix seconds in
`rejected_source_timestamp`, local `received_at`, and
`max_future_skew_seconds`. `source_timestamp` still describes the retained
snapshot and is omitted when none exists. Future rejection warns once per
outage, including one additional warning if another error opened that outage.
Further repetitions are suppressed until a successful publication resets the
outage and emits the existing INFO recovery event. Every rejection increments
the feed's error counter without advancing last success, published timestamp,
or train count; shutdown cancellation remains suppressed.

For persistent rejection, compare the logged source and receipt timestamps
and check host clock synchronization. Correct host time if it is wrong;
otherwise await upstream recovery. If a process already holds a poisoned
snapshot accepted by the old code, a deployment restart clears that in-memory
state. For local logs and metrics
in Grafana, see [the LGTM environment](ops/lgtm/README.md).

## Protobuf bindings

Bindings under `internal/gen/gtfs` are generated and committed so normal builds
do not require protobuf tooling. Their immutable upstream revisions and hashes
are recorded in `proto/SOURCES`. Regeneration requires the exact `protoc` and
`protoc-gen-go` versions checked by `proto/generate.sh`:

```sh
PROTOC=/path/to/protoc PROTOC_GEN_GO=/path/to/protoc-gen-go ./proto/generate.sh
```

Shape interpolation and schedule fallback rendering remain a later delivery
slice. Stale last-known-good realtime data remains
queryable with `freshness: "stale"`; `/readyz` returns `503` only when no feed
is fresh.
