#!/usr/bin/env bash
# Regenerates the Go code for the vendored monty.v1 proto into
# remote/internal/montyv1.
#
# The plugin versions are pinned below so the output is byte-stable across
# contributors and CI ("make check-proto" regenerates and diffs). protoc only
# parses the .proto; its version is stripped from the generated headers because
# it does not change the output and distros ship different releases.
set -euo pipefail

PROTOC_GEN_GO_VERSION=v1.36.12
PROTOC_GEN_GO_GRPC_VERSION=v1.6.2

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin="$root/.bin"
out="$root/remote/internal/montyv1"
module="github.com/mdfranz/gomonty/remote/internal/montyv1"

command -v protoc >/dev/null || { echo "protoc is required (apt install protobuf-compiler / brew install protobuf)" >&2; exit 1; }

mkdir -p "$bin"
GOBIN="$bin" go install "google.golang.org/protobuf/cmd/protoc-gen-go@${PROTOC_GEN_GO_VERSION}"
GOBIN="$bin" go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@${PROTOC_GEN_GO_GRPC_VERSION}"

mkdir -p "$out"
rm -f "$out"/*.pb.go

# The vendored file is byte-identical to upstream and sets no go_package, so the
# Go import path is supplied here instead.
PATH="$bin:$PATH" protoc \
  --proto_path="$root/remote/proto" \
  --go_out="$out" --go_opt=paths=source_relative \
  --go_opt="Mmonty/v1/monty.proto=$module" \
  --go-grpc_out="$out" --go-grpc_opt=paths=source_relative \
  --go-grpc_opt="Mmonty/v1/monty.proto=$module" \
  monty/v1/monty.proto

# paths=source_relative keeps the monty/v1 directory; flatten it.
mv "$out"/monty/v1/*.pb.go "$out"/
rm -rf "$out/monty"

# Strip the protoc version from the generated headers.
sed -i.bak -E 's|^(//[[:space:]]+(- )?protoc[[:space:]]+).*$|\1(version not recorded)|' "$out"/*.pb.go
rm -f "$out"/*.pb.go.bak

echo "Generated $(ls "$out"/*.pb.go | wc -l | tr -d ' ') files in remote/internal/montyv1"
