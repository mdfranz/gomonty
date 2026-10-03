# Release Process

## Upstream Monty Pin

`crates/monty-go-ffi` builds against upstream Monty through the pinned git
dependencies in the root `Cargo.toml`:

- `monty`
- `monty_type_checking`
- `monty_types`

To bump the upstream dependency:

1. Update the `rev` for all three dependencies in `Cargo.toml` (they must use the same full 40-character commit hash).
2. Refresh `Cargo.lock` with `cargo update`.
3. Rebuild the host shared library and run local verification:

```bash
MONTY_GO_FFI_SKIP_HEADER=1 scripts/build-go-ffi.sh aarch64-apple-darwin
CGO_ENABLED=0 go test ./...
```

If you want to develop against a local Monty checkout instead of the pinned git
dependency, use a temporary Cargo patch:

```toml
[patch."https://github.com/pydantic/monty.git"]
monty = { path = "../monty/crates/monty" }
monty_type_checking = { path = "../monty/crates/monty-type-checking" }
```

## Release Workflow

The release flow has two explicit steps.

1. Prepare the release PR:

```bash
make release
```

That target fetches tags from `origin`, computes the next patch release from the
latest semver tag (for example `v0.0.13` -> `v0.0.14`), and dispatches the
`release-prep` GitHub Actions workflow on `main`. If you need to override the
version explicitly, use `make release VERSION=vX.Y.Z`. The workflow:

- validates the requested version and ensures the tag does not already exist
- rebuilds the tracked shared libraries for:
  - `darwin/arm64`
  - `linux/amd64` (GNU/glibc)
  - `linux/arm64` (GNU/glibc)
  - `linux/amd64` (musl/Alpine)
  - `linux/arm64` (musl/Alpine)
  - `windows/amd64`
- regenerates `internal/ffi/include/monty_go_ffi.h` exactly once
- updates `Cargo.toml`, `Cargo.lock`, `internal/ffi/lib/...`, and `internal/ffi/checksums.txt`
- reruns release validation on the assembled tree:
  - `CGO_ENABLED=0 go test ./...`
  - `go vet ./...`
  - `cd examples && CGO_ENABLED=0 go run ./cmd/example`
- commits the release tree to a `release-prep/vX.Y.Z` branch
- opens a pull request back to `main`

After that PR merges, publish the release from the merged `main` commit:

```bash
make publish-release VERSION=vX.Y.Z
```

That target dispatches the `release` GitHub Actions workflow on `main`. The
workflow does **not** rebuild anything. Rust FFI builds aren't reproducible, so a
rebuild would produce binaries that differ from the committed ones that `go get`
users receive. Instead, it publishes exactly what release-prep committed. The
workflow:

- validates the requested version and ensures the tag does not already exist
- checks that `Cargo.toml`'s version matches the requested version
- runs `sha256sum --check` against `internal/ffi/checksums.txt`, which covers
  `Cargo.lock`, the header and all six shared libraries. This fails if anything
  release-prep produced has changed since, for example an upstream pin bump
  merged after the release-prep PR. In that case, re-run release-prep.
- runs release validation against the committed libraries:
  - `CGO_ENABLED=0 go test ./...`
  - `go vet ./...`
  - `cd examples && CGO_ENABLED=0 go run ./cmd/example`
- tags the exact commit the workflow was dispatched on, so a merge to `main`
  during the run can't change what gets tagged
- creates the GitHub release with the committed shared libraries and
  `checksums.txt` attached, and generates release notes from the exact git range
  since the previous tag
- warms the Go module proxy with `go list -m`, which is the trigger `pkg.go.dev`
  and the module mirror need

This flow assumes GitHub Actions can push tags and create releases. It does not
require Actions to bypass the repository rule that changes to `main` must land
through a pull request.

Current CI coverage:

- native `CGO_ENABLED=0` Go tests on Linux, macOS, and Windows
- build verification for musl Linux shared libraries
- `go vet ./...`
- a smoke run of the standalone example module under `examples/`

## Why The Shared Libraries Must Be Committed Before Tagging

Go module consumers fetch the tagged source tree. They do not fetch GitHub
release assets as part of `go get`.

Because the current runtime loader embeds the checked-in shared libraries under
`internal/ffi/lib/<target>`, the tag itself must already contain the correct
shared libraries and header. Release assets are optional convenience copies
only.

## Manual Shared Library Refresh

If you need to refresh artifacts locally instead of using the workflow, build
each supported target explicitly on a compatible host:

```bash
scripts/build-go-ffi.sh aarch64-apple-darwin
scripts/build-go-ffi.sh aarch64-unknown-linux-gnu
scripts/build-go-ffi.sh aarch64-unknown-linux-musl
scripts/build-go-ffi.sh x86_64-unknown-linux-gnu
scripts/build-go-ffi.sh x86_64-unknown-linux-musl
scripts/build-go-ffi.sh x86_64-pc-windows-msvc
```

Commit the updated files:

- `Cargo.toml`
- `Cargo.lock`
- `internal/ffi/include/monty_go_ffi.h`
- `internal/ffi/lib/...` shared libraries only
- `internal/ffi/checksums.txt`

## Post-Release Verification

After the publish workflow completes:

1. Verify the GitHub release exists for the requested tag.
2. Verify the tagged package pages on `pkg.go.dev`, including package docs,
   examples, and detected license metadata.
3. If the publish workflow fails after pushing the tag, fix forward with a new
   version rather than mutating an existing tag.
