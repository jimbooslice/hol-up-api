# Local Grafana LGTM

```text
API stdout ──> Alloy ──> Loki ──> Grafana
API /metrics ──────────> Prometheus ──> Grafana
```

Run from this directory with Docker Compose v2:

```sh
docker compose up -d --build
docker compose ps
docker compose logs -f api
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:12345/-/ready
docker compose down       # preserve telemetry and Alloy positions
docker compose down -v    # reset local data, including Grafana credentials
```

Open Grafana at <http://127.0.0.1:3000>, sign in with `admin/admin`, and use
Explore with the provisioned Loki or Prometheus data source. Anonymous access
and sign-up are disabled. Set `GRAFANA_ADMIN_USER` and
`GRAFANA_ADMIN_PASSWORD` before the first start to override credentials; existing
Grafana data retains its credentials. Host ports are loopback-only and can be
changed with `API_PORT`, `GRAFANA_PORT`, and `ALLOY_PORT`.

The API has no dependency on the telemetry services: health, readiness, partial
feed responses, and JSON stdout continue if Alloy or LGTM stops. Readiness still
depends on fresh MTA feeds and may fail when upstream access is unavailable.

## Components and storage

The API is built from the repository `Containerfile`. Only the API has the
collection label. Alloy discovers containers in the same Compose project over
the runtime socket, reads stdout without rewriting JSON, and sends it to Loki.
Only `service_name="hol-up-api"` and `environment="local"` are indexed labels;
`level`, `event`, and `feed` are parsed when querying. Alloy positions persist in
`alloy-data`. Its ordinary retries are best-effort; no durable delivery WAL is
configured, so outages can lose diagnostic logs.

The audited pin [grafana/otel-lgtm:0.30.0](https://github.com/grafana/docker-otel-lgtm/tree/v0.30.0)
bundles Grafana, Loki, Prometheus, Tempo, Pyroscope, and the OpenTelemetry
Collector. `prometheus.yaml` preserves that release's upstream configuration and
adds a 15-second API scrape. Prometheus is the metrics backend; Mimir is absent.
Loki and Prometheus retain seven days of telemetry in the `lgtm-data` volume
mounted at `/data`. Loki retention enables the compactor and filesystem
request store; deletion is asynchronous. This is time retention, not a disk
quota. Grafana state is also persisted. Upgrades must compare the pinned
upstream configuration and rerun the smoke test.

Tempo (traces), Pyroscope (profiles), and the collector (OTLP receiver) run but
receive no application instrumentation. Their ports, along with Loki and
Prometheus, remain internal. The healthcheck requires all six bundled processes
to report readiness. Alloy's readiness/UI is available on port 12345.

## Rootless Podman

Podman's `compose` command needs an external provider (`docker-compose` v2 or
`podman-compose`) installed on PATH, or selected with `PODMAN_COMPOSE_PROVIDER`.
Activate the [user API socket](https://docs.podman.io/en/latest/markdown/podman-system-service.1.html):

```sh
systemctl --user start podman.socket
export CONTAINER_SOCKET="${XDG_RUNTIME_DIR}/podman/podman.sock"
podman compose up -d --build
podman compose ps
podman compose down
```

`CONTAINER_SOCKET` defaults to `/var/run/docker.sock` for Docker and is mounted
there inside Alloy. On SELinux, Alloy uses `label=disable` to access the socket;
the Prometheus configuration bind uses `:Z`. Do not relabel the runtime socket.
Socket access grants control over the runtime and host resources available to
its owner, even with a read-only mount. This Unix-socket environment is for
trusted local development, not shared or production exposure.

## Explore queries

Loki (parse fields at query time):

```logql
{service_name="hol-up-api", environment="local"} | json
{service_name="hol-up-api"} | json | level="WARN"
{service_name="hol-up-api"} | json | event="realtime_feed_refresh_recovered"
{service_name="hol-up-api"} | json | feed="ace"
```

Prometheus:

```promql
up{job="hol-up-api"}
holup_realtime_feed_fresh{job="hol-up-api"}
sum by (feed) (rate(holup_realtime_feed_refreshes_total{outcome="error"}[5m]))
```

JSON logs include `service="hol-up-api"`. Lifecycle events are `api_starting`,
`api_shutdown_started`, and `api_stopped`. Each feed outage produces one
`realtime_feed_refresh_failed` warning, followed by one
`realtime_feed_refresh_recovered` info event with failure count and outage
duration. Repeated failures still increment metrics. Initial failures explicitly
report no snapshot and omit its timestamp. Shutdown cancellation records neither
refresh outcomes nor transitions. Feed URLs and request data are not logged.

## Verification

Requires Bash, curl, jq, a running runtime, and its Compose provider:

```sh
./smoke.sh
CONTAINER_RUNTIME=podman ./smoke.sh
```

The smoke uses an isolated project and ports 18080, 13000, and 12346 (overridable
with the same port variables). It validates Alloy, Prometheus, and Loki
configuration, checks health, log ingestion, metrics, provisioned data sources,
persistence across restart, new telemetry after restart, loopback bindings, and
API operation with telemetry stopped. Its trap removes the isolated volumes.

OBI, application tracing/profiling, alerts, custom dashboards, HTTP access logs,
Mimir, and production deployment remain deferred. A smaller Grafana/Loki/Alloy
stack would cover logging alone; this environment follows the selected full
bundle.
