#!/bin/sh
# Generates repository-owned Go bindings for the pinned schemas in SOURCES.
# Required tools: protoc 27.5 and protoc-gen-go v1.36.12.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
protoc_bin=${PROTOC:-protoc}
protoc_gen_go=${PROTOC_GEN_GO:-protoc-gen-go}

[ "$($protoc_bin --version)" = "libprotoc 27.5" ]
[ "$($protoc_gen_go --version)" = "protoc-gen-go v1.36.12" ]

exec "$protoc_bin" \
  --plugin="protoc-gen-go=$protoc_gen_go" \
  --proto_path="$root/proto/upstream" \
  --go_out="$root" \
  --go_opt=module=hol-up-prototype \
  --go_opt=Mcom/google/transit/realtime/gtfs-realtime.proto=hol-up-prototype/internal/gen/gtfs \
  --go_opt=Mnyct-subway.proto=hol-up-prototype/internal/gen/gtfs \
  com/google/transit/realtime/gtfs-realtime.proto \
  nyct-subway.proto
