#!/usr/bin/env bash
# Runs remote/realworker_test.go against a real Monty worker on this machine.
#
# It builds the `monty` binary from the upstream commit pinned in Cargo.toml
# (cached, so only the first run compiles), starts upstream's
# scripts/grpc_relay.py from that same commit on an ephemeral localhost port,
# runs the tests against it and stops the relay. No credentials are involved.
#
# Needs git, cargo and uv. Set MONTY_BIN to use an existing `monty` binary
# built from the pinned commit. Extra arguments go to `go test`.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
rev="$(sed -n 's/^monty = .*rev = "\([0-9a-f]\{40\}\)".*/\1/p' "$root/Cargo.toml")"
[[ -n "$rev" ]] || { echo "could not read the monty rev from Cargo.toml" >&2; exit 1; }

for tool in git uv go; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done

cache="${XDG_CACHE_HOME:-$HOME/.cache}/gomonty/monty-$rev"
src="$cache/src"
if [[ ! -d "$src/.git" ]]; then
  mkdir -p "$src"
  git -C "$src" init -q
  git -C "$src" remote add origin https://github.com/pydantic/monty.git
fi
if ! git -C "$src" cat-file -e "$rev^{commit}" 2>/dev/null; then
  git -C "$src" fetch -q --depth 1 origin "$rev"
fi
git -C "$src" checkout -q --detach "$rev"

monty_bin="${MONTY_BIN:-$src/target/release/monty}"
if [[ ! -x "$monty_bin" ]]; then
  command -v cargo >/dev/null || { echo "cargo is required to build monty" >&2; exit 1; }
  echo "building monty at $rev (first run only; takes several minutes)" >&2
  (cd "$src" && cargo build --release -p monty-runtime --bin monty)
fi

relay_log="$(mktemp)"
relay_pid=""
cleanup() {
  [[ -n "$relay_pid" ]] && kill "$relay_pid" 2>/dev/null || true
  rm -f "$relay_log"
}
trap cleanup EXIT

uv run "$src/scripts/grpc_relay.py" --host 127.0.0.1 --port 0 --monty-bin "$monty_bin" >"$relay_log" 2>&1 &
relay_pid=$!

url=""
for _ in $(seq 1 60); do
  url="$(grep -m1 '^http://' "$relay_log" || true)"
  [[ -n "$url" ]] && break
  kill -0 "$relay_pid" 2>/dev/null || { cat "$relay_log" >&2; echo "relay exited early" >&2; exit 1; }
  sleep 1
done
[[ -n "$url" ]] || { cat "$relay_log" >&2; echo "relay did not report a URL" >&2; exit 1; }

cd "$root/remote"
MONTY_REMOTE_ENDPOINT="${url#http://}" MONTY_REMOTE_INSECURE=1 \
  go test -count=1 -run RealWorker -v "$@" .
