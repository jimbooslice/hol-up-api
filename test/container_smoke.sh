#!/bin/sh
set -eu

if [ -n "${CONTAINER_RUNTIME:-}" ]; then
    runtime=$CONTAINER_RUNTIME
elif command -v docker >/dev/null 2>&1; then
    runtime=docker
elif command -v podman >/dev/null 2>&1; then
    runtime=podman
else
    echo "docker or podman is required" >&2
    exit 1
fi

port=${SMOKE_PORT:-18080}
image="hol-up-api-smoke-$$"
container="hol-up-api-smoke-$$"

cleanup() {
    "$runtime" rm -f "$container" >/dev/null 2>&1 || true
    "$runtime" image rm "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

"$runtime" build -f Containerfile -t "$image" .
"$runtime" run -d --stop-signal SIGTERM --name "$container" -p "127.0.0.1:$port:8080" \
    -e MTA_GTFS_SUPPLEMENTED_URL=http://127.0.0.1:1/supplemented \
    -e MTA_GTFS_REGULAR_URL=http://127.0.0.1:1/regular \
    -e MTA_GTFS_RT_URL=http://127.0.0.1:1/realtime \
    -e MTA_GTFS_RT_BDFM_URL=http://127.0.0.1:1/realtime \
    -e MTA_GTFS_RT_G_URL=http://127.0.0.1:1/realtime \
    -e MTA_GTFS_RT_JZ_URL=http://127.0.0.1:1/realtime \
    -e MTA_GTFS_RT_L_URL=http://127.0.0.1:1/realtime \
    -e MTA_GTFS_RT_NQRW_URL=http://127.0.0.1:1/realtime \
    -e MTA_GTFS_RT_NUMBERED_URL=http://127.0.0.1:1/realtime \
    -e MTA_GTFS_RT_SI_URL=http://127.0.0.1:1/realtime "$image" >/dev/null

attempt=0
while [ "$attempt" -lt 20 ]; do
    if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null; then
        for path in readyz api/v1/routes 'api/v1/trains?route_id=A' 'api/v1/trains?route_id=A&view=positions' api/v1/routes/A/geometry; do
            test "$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/$path")" = 503
        done
        curl -fsS "http://127.0.0.1:$port/metrics" >/dev/null
        curl -fsS "http://127.0.0.1:$port/openapi.yaml" >/dev/null
        "$runtime" stop --time 15 "$container" >/dev/null
        "$runtime" logs "$container" | jq -se '
          all(.[]; .service == "hol-up-api") and
          ([.[] | select(.event == "api_starting" and .level == "INFO" and .address == ":8080" and .feed_count == 8 and .poll_interval_seconds == 30 and .stale_after_seconds == 60 and .max_future_skew_seconds == 30)] | length == 1) and
          ([.[] | select(.event == "schedule_refresh_failed" and .level == "WARN" and (.source == "supplemented" or .source == "regular"))] | length == 2) and
          ([.[] | select(.event == "schedule_fallback" and .reason == "no_supplemented_schedule")] | length == 1) and
          ([.[] | select(.event == "api_shutdown_started" and .timeout_seconds == 10)] | length == 1) and
          ([.[] | select(.event == "api_stopped" and .level == "INFO" and (has("error") | not))] | length == 1)
        ' >/dev/null
        test "$("$runtime" inspect --format '{{.State.ExitCode}}' "$container")" = 0
        exit 0
    fi
    attempt=$((attempt + 1))
    sleep 1
done

"$runtime" logs "$container" >&2 || true
echo "health check failed" >&2
exit 1
