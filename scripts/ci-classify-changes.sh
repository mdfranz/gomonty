#!/usr/bin/env bash
set -euo pipefail

# Classify newline-separated repository-relative paths read from stdin into
# GitHub Actions workflow tiers:
#   code=<true|false>   - run core build/test/lint jobs (go-pure)
#   proto=<true|false>  - run check-proto (remote module + proto drift)
#   native=<true|false> - run multi-platform native builds (linux-arm64, darwin-arm64)
#
# Uncertain or unrecognized files fall back conservatively to all three=true.

code=false
proto=false
native=false
saw_path=false

has_build_constraint() {
  local file="$1"
  [ -f "$file" ] && grep -qE '^//go:build[[:space:]]' "$file"
}

while IFS= read -r path || [ -n "$path" ]; do
  path="${path%$'\r'}"
  [ -z "$path" ] && continue
  saw_path=true

  case "$path" in
    # Native-affecting, workflow, build, or module files always select the full native matrix.
    crates/*|Cargo.toml|Cargo.lock|rust-toolchain.toml|internal/ffi/*|scripts/*|.github/*|go.mod|go.sum|Makefile|wire.go|wire_test.go|wire_negzero_test.go|smoke_test.go)
      code=true
      proto=true
      native=true
      ;;
    *_windows*.go|*_darwin*.go|*_linux*.go|*_musl*.go|*_unix*.go)
      code=true
      proto=true
      native=true
      ;;
    # Documentation-only files leave all build/test tiers unchanged.
    *.md|docs/*|.agents/*|.claude/*|LICENSE|LICENSE.*|.gitignore)
      ;;
    # Non-native Go packages, examples, proto/gRPC module, Python scripts, and fixtures.
    remote/*|otelmonty/*|cmd/shmonty/*|examples/*|vfs/*|python-scripts/*|testdata/*)
      code=true
      proto=true
      ;;
    # Top-level *.go files (excluding wire/smoke files and OS-suffixed files handled above)
    # stay non-native unless the file declares a //go:build constraint.
    *.go)
      case "$path" in
        */*)
          code=true
          proto=true
          native=true
          ;;
        *)
          code=true
          proto=true
          if has_build_constraint "$path"; then
            native=true
          fi
          ;;
      esac
      ;;
    # Any unrecognized path conservatively enables all tiers.
    *)
      code=true
      proto=true
      native=true
      ;;
  esac
done

if [ "$saw_path" = false ]; then
  code=true
  proto=true
  native=true
fi

echo "code=$code"
echo "proto=$proto"
echo "native=$native"
