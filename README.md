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

## Verify

```sh
go test ./...
go test -race ./...
go vet ./...
./test/container_smoke.sh
```

The container build runs the Go tests before producing a non-root runtime
image. The smoke script uses Docker or Podman and checks `/healthz`.

Failure/recovery logging improvements and local Grafana/Loki containers are
intentionally deferred to the next delivery phase.

## Protobuf bindings

Bindings under `internal/gen/gtfs` are generated and committed so normal builds
do not require protobuf tooling. Their immutable upstream revisions and hashes
are recorded in `proto/SOURCES`. Regeneration requires the exact `protoc` and
`protoc-gen-go` versions checked by `proto/generate.sh`:

```sh
PROTOC=/path/to/protoc PROTOC_GEN_GO=/path/to/protoc-gen-go ./proto/generate.sh
```

Static GTFS schedule ingestion, shape interpolation, and schedule fallback are
a later delivery slice. Until then, stale last-known-good realtime data remains
queryable with `freshness: "stale"`; `/readyz` returns `503` only when no feed
is fresh.
