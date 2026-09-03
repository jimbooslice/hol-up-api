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
"$runtime" run -d --name "$container" -p "127.0.0.1:$port:8080" "$image" >/dev/null

attempt=0
while [ "$attempt" -lt 20 ]; do
    if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null; then
        exit 0
    fi
    attempt=$((attempt + 1))
    sleep 1
done

"$runtime" logs "$container" >&2 || true
echo "health check failed" >&2
exit 1
