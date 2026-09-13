#!/usr/bin/env bash
set -Eeuo pipefail
trap 'echo "Smoke failed at line $LINENO" >&2' ERR
cd "$(dirname "$0")"
if [[ -n ${CONTAINER_RUNTIME:-} ]]; then
    runtime=$CONTAINER_RUNTIME
elif command -v docker >/dev/null; then
    runtime=docker
else
    runtime=podman
fi
export COMPOSE_PROJECT_NAME="holup-smoke-$$"
export API_PORT=${API_PORT:-18080} GRAFANA_PORT=${GRAFANA_PORT:-13000} ALLOY_PORT=${ALLOY_PORT:-12346}
export GRAFANA_ADMIN_USER=${GRAFANA_ADMIN_USER:-admin} GRAFANA_ADMIN_PASSWORD=${GRAFANA_ADMIN_PASSWORD:-admin}
if [[ $runtime == podman ]]; then
    export CONTAINER_SOCKET=${CONTAINER_SOCKET:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/podman/podman.sock}
fi
compose() { "$runtime" compose -f compose.yaml "$@"; }
cleanup() {
    status=$?
    if (( status != 0 )); then compose logs --tail=100 >&2 || true; fi
    compose down -v --remove-orphans >/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
wait_for() {
    local deadline=$((SECONDS + 300))
    until "$@"; do
        if (( SECONDS >= deadline )); then echo "Timed out: $*" >&2; return 1; fi
        sleep 3
    done
}
backend_get() { compose exec -T lgtm curl -fsS --get "$@"; }
loki_query() {
    backend_get http://127.0.0.1:3100/loki/api/v1/query_range \
        --data-urlencode 'query={service_name="hol-up-api"} | json | event="api_starting"' \
        --data-urlencode "start=$start" --data-urlencode 'limit=1000'
}
prom_query() { backend_get http://127.0.0.1:9090/api/v1/query --data-urlencode "query=$1"; }
healthy() {
    local id
    for service in api lgtm; do
        id=$(compose ps -q "$service")
        [[ -n $id && $("$runtime" inspect --format '{{.State.Health.Status}}' "$id") == healthy ]] || return 1
    done
    curl -fsS "http://127.0.0.1:$ALLOY_PORT/-/ready" >/dev/null
}
logs_arrived() { loki_query | jq -e '.data.result | length > 0' >/dev/null; }
metrics_arrived() {
    prom_query 'up{job="hol-up-api"} == 1' | jq -e '.data.result | length > 0' >/dev/null &&
    prom_query '{__name__=~"holup_realtime_.+"}' | jq -e '.data.result | length > 0' >/dev/null
}
compose config --quiet
compose run --rm --no-deps alloy validate /etc/alloy/config.alloy
compose run --rm --no-deps --entrypoint /otel-lgtm/prometheus/promtool lgtm check config /otel-lgtm/prometheus.yaml
compose run --rm --no-deps --entrypoint /bin/bash lgtm -c '
    read -ra flags <<< "$LOKI_EXTRA_ARGS"
    /otel-lgtm/loki/loki -config.file=/otel-lgtm/loki-config.yaml "${flags[@]}" -verify-config=true
'
start=$(date +%s)000000000
compose up -d --build
wait_for healthy
compose restart api
wait_for logs_arrived
wait_for metrics_arrived
# The series API reports indexed labels. Query results also expose Loki's
# automatically detected level as structured metadata, which is not indexed.
backend_get http://127.0.0.1:3100/loki/api/v1/series \
    --data-urlencode 'match[]={service_name="hol-up-api"}' --data-urlencode "start=$start" |
    jq -e '(.data | length > 0) and all(.data[]; keys == ["environment", "service_name"])' >/dev/null
backend_get http://127.0.0.1:3100/loki/api/v1/query_range \
    --data-urlencode 'query={service_name="hol-up-api"}' --data-urlencode "start=$start" |
    jq -e 'any(.data.result[].values[]; (.[1] | fromjson | .event == "api_starting" and .service == "hol-up-api"))' >/dev/null
backend_get http://127.0.0.1:9090/api/v1/status/flags |
    jq -e '.data["storage.tsdb.retention.time"] == "1w"' >/dev/null
curl -fsS -u "$GRAFANA_ADMIN_USER:$GRAFANA_ADMIN_PASSWORD" "http://127.0.0.1:$GRAFANA_PORT/api/datasources" |
    jq -e 'any(.[]; .type == "loki") and any(.[]; .type == "prometheus")' >/dev/null
old_log=$(loki_query | jq -r '.data.result[0].values[0][0]')
old_time=$(date +%s)
# Test old samples at a fixed evaluation time after restarting storage.
compose restart lgtm
wait_for healthy
loki_query | jq -e --arg timestamp "$old_log" 'any(.data.result[].values[]; .[0] == $timestamp)' >/dev/null
backend_get http://127.0.0.1:9090/api/v1/query --data-urlencode 'query=up{job="hol-up-api"} == 1' \
    --data-urlencode "time=$old_time" | jq -e '.data.result | length > 0' >/dev/null
new_start=$(date +%s)000000000
compose restart api
new_logs_arrived() { loki_query | jq -e --arg timestamp "$new_start" 'any(.data.result[].values[]; .[0] >= $timestamp)' >/dev/null; }
new_metrics_arrived() {
    prom_query 'timestamp(up{job="hol-up-api"} == 1)' |
        jq -e --argjson cutoff "${new_start%000000000}" 'any(.data.result[]; ( .value[1] | tonumber ) >= $cutoff)' >/dev/null
}
wait_for new_logs_arrived
wait_for new_metrics_arrived
# Inspect actual runtime port bindings, including unexpected published ports.
ids=$(compose ps -q)
# Container IDs contain no whitespace; intentional splitting supplies separate arguments.
# shellcheck disable=SC2086
"$runtime" inspect $ids | jq -e '
  [.[].NetworkSettings.Ports // {} | to_entries[] | .value[]?] as $ports |
  ($ports | length == 3) and all($ports[]; .HostIp == "127.0.0.1")' >/dev/null
compose stop alloy lgtm
curl -fsS "http://127.0.0.1:$API_PORT/healthz" >/dev/null
curl -fsS "http://127.0.0.1:$API_PORT/metrics" >/dev/null
compose restart api
wait_for curl -fsS "http://127.0.0.1:$API_PORT/healthz"
compose logs --no-color api | grep '"event":"api_starting"' >/dev/null
echo 'LGTM smoke passed: logs, metrics, data sources, persistence, loopback ports, and API independence.'
