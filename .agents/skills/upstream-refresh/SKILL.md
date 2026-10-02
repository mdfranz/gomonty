---
name: upstream-refresh
description: Refresh gomonty Go bindings against upstream pydantic/monty. Use this skill whenever the user wants to bump the monty dependency, add support for new upstream types, update the FFI wire format, sync with upstream changes, or prepare a release after upstream moves forward. Trigger on phrases like "upstream refresh", "bump monty", "new upstream types", "sync with upstream", "update monty pin", "release prep", or any mention of pydantic/monty changes that need to be reflected in gomonty.
---

# Upstream Refresh for gomonty

This skill walks through refreshing the Go bindings in gomonty against a newer version of the upstream [pydantic/monty](https://github.com/pydantic/monty) Python interpreter.

gomonty wraps upstream Monty through a Rust FFI crate (`crates/monty-go-ffi`) that serializes values across the boundary using a versioned MessagePack wire format. When upstream adds new types or changes its `MontyObject` enum, the wire format and Go bindings must be updated to match.

## Overview

The refresh touches four layers, always in this order:

```
Upstream Monty (MontyObject enum)
    |
    v
Rust FFI wire format (crates/monty-go-ffi/src/wire.rs)
    |
    v
Go wire format (wire.go)
    |
    v
Go public types (types.go)
```

Changes flow top-down. Never skip a layer — if upstream adds a variant, all four layers need updates.

## Step 1: Discover upstream changes

Find the currently pinned revision in `Cargo.toml` (the `rev` field on the `monty` dependency). Target the latest upstream **release tag** by default; only target `main` when you specifically need unreleased changes.

```bash
# Get the pinned rev
grep 'rev = ' Cargo.toml

# Find the latest release
gh release list -R pydantic/monty --limit 5

# List commits since the pin (use the tag, or main)
gh api 'repos/pydantic/monty/compare/<pinned-rev>...<tag>' \
  --jq '.commits[] | "\(.sha[0:12]) \(.commit.message | split("\n")[0])"'

# Read the release notes for the range
gh release view <tag> -R pydantic/monty
```

Get the full 40-character SHA of the target; Cargo can't resolve truncated SHAs:

```bash
gh api 'repos/pydantic/monty/commits/<tag>' --jq .sha
```

## Step 2: Classify changes by FFI impact

Not every upstream commit affects the Go bindings. Classify each commit:

**FFI-affecting** (requires wire format + Go changes):
- New `MontyObject` variants (new Python types like `Date`, `DateTime`, etc.)
- Changed fields on existing variants (renamed/added/removed fields)
- New `ExcType` variants
- Changes to `run_progress.rs`, `object.rs`, or the public API surface

**Internal-only** (just bump the pin, no binding changes):
- New builtin modules that don't introduce new types (e.g., `json`, `math`)
- New string/bytes methods
- Compiler/VM optimizations
- Heap/memory management refactors
- CI/tooling changes

To identify FFI-affecting changes, check the PR file lists for changes to `object.rs`, `run_progress.rs`, and `convert.rs` (the Python/JS converters — if they handle a new variant, we need to as well):

```bash
gh api 'repos/pydantic/monty/pulls/<PR-number>/files' --jq '.[].filename'
```

For new `MontyObject` variants, read the struct definitions from upstream to understand the exact fields:

```bash
gh api 'repos/pydantic/monty/contents/crates/monty/src/object.rs?ref=main' \
  --jq '.content' | base64 -d | grep -B2 -A20 'pub struct MontyFoo'
```

## Step 3: Bump the upstream pin

Update all three `rev` values in `Cargo.toml` (they must stay aligned):

```toml
monty = { git = "https://github.com/pydantic/monty.git", rev = "<full-sha>" }
monty_type_checking = { git = "https://github.com/pydantic/monty.git", package = "monty-type-checking", rev = "<full-sha>" }
monty_types = { git = "https://github.com/pydantic/monty.git", package = "monty-types", rev = "<full-sha>" }
```

Use the **full 40-character SHA**; Cargo won't resolve truncated SHAs. Then refresh the lockfile. `cargo update` takes the hyphenated **package** names, not the dependency keys:

```bash
cargo update -p monty -p monty-type-checking -p monty-types
```

Read the `cargo update` output: new crates appearing (for example `minicbor` when upstream changed its dump encoding) hint at serialization changes to check in Step 7.

## Step 4: Update the Rust wire format

File: `crates/monty-go-ffi/src/wire.rs`

This file defines the binary wire protocol between Rust and Go. For each new or changed `MontyObject` variant:

### 4a: Wire constants

Add new `WIRE_VALUE_*` constants, continuing the sequence after the last existing constant:

```rust
pub const WIRE_VALUE_NEW_TYPE: u8 = <next-number>;
```

### 4b: WireValue fields

Add fields to the `WireValue` struct for any new data the type carries. Use `serde` skip attributes to keep the wire format compact:

```rust
#[serde(default, skip_serializing_if = "is_zero_i32")]
pub new_field: i32,
```

Reuse existing fields where the semantics match (e.g., `string_value` for string-like data). Only add new fields when the type carries data that doesn't map to any existing field.

Add any needed zero-check helper functions (`is_zero_*`), but check for duplicates first — the file already has helpers for common types.

### 4c: from_monty / into_monty

Add match arms in both directions:

- `from_monty`: converts `MontyObject` -> `WireValue` (serialization, Rust to Go)
- `into_monty`: converts `WireValue` -> `MontyObject` (deserialization, Go to Rust)

For output-only types (like `Repr` or `Cycle`), `into_monty` should return an error since they can't be used as inputs.

### 4d: Update imports

Add any new types to the `use monty::{...}` import at the top of the file, and in the `#[cfg(test)] mod tests` block.

### 4e: Rust tests

Add round-trip tests for each new type. The pattern is:

```rust
#[test]
fn wire_value_round_trips_new_type() {
    let original = MontyObject::NewType(MontyNewType { ... });
    let decoded = WireValue::from_monty(&original)
        .into_monty()
        .expect("new type should round-trip");
    assert_eq!(decoded, original);
}
```

Test edge cases: zero values, optional fields as `None` vs `Some`, negative values where applicable.

### Handling removed or renamed variants

If upstream removes a `MontyObject` variant:
- Remove the corresponding `WIRE_VALUE_*` constant, but do NOT renumber existing constants (wire compatibility)
- Remove the `from_monty` / `into_monty` arms
- Remove any `WireValue` fields that are no longer used by any variant

If upstream renames fields on an existing variant:
- Update the `from_monty` / `into_monty` arms to use the new field names
- The `WireValue` field names (which are the msgpack keys) can stay the same to maintain wire compatibility, or bump `WIRE_VERSION` if a breaking change is needed

## Step 5: Update the Go wire format

File: `wire.go`

Mirror every change from Step 4:

### 5a: Wire constants

```go
const (
    // ... existing constants using iota ...
    wireValueNewType
)
```

The Go constants use `iota` so they auto-number — just add new ones at the end in the same order as the Rust constants.

### 5b: wireValue fields

```go
type wireValue struct {
    // ... existing fields ...
    NewField int32 `msgpack:"new_field,omitempty"`
}
```

Field names and msgpack tags must match the Rust `WireValue` serde names exactly.

### 5c: wireValueFromPublic / toPublic

Add cases in both `wireValueFromPublic` (Go Value -> wireValue) and `toPublic` (wireValue -> Go Value) for each new type. Place them before the `default` case.

## Step 6: Update Go public types

File: `types.go`

### 6a: Value kinds

```go
const (
    // ... existing kinds ...
    valueKindNewType ValueKind = "new_type"
)
```

### 6b: Struct definitions

Define a Go struct for each new upstream type. Match the upstream field names and types:

| Rust type | Go type |
|-----------|---------|
| `i32` | `int32` |
| `u8` | `uint8` |
| `u32` | `uint32` |
| `i64` | `int64` |
| `f64` | `float64` |
| `String` | `string` |
| `Option<T>` | `*T` |
| `Vec<T>` | `[]T` |

Include JSON struct tags for the JSON marshal/unmarshal path.

### 6c: Value constructor

```go
func NewTypeValue(val NewType) Value {
    return Value{kind: valueKindNewType, data: val}
}
```

### 6d: Accessor method

```go
func (v Value) NewType() (NewType, bool) {
    value, ok := v.data.(NewType)
    return value, ok
}
```

### 6e: ValueOf case

Add a case in the `ValueOf` switch for the new Go struct type.

### 6f: JSON MarshalJSON / UnmarshalJSON

Add cases in both `MarshalJSON` and `UnmarshalJSON` on the `Value` type. Follow the existing pattern — marshal with an inline struct containing a `Kind` field, unmarshal by switching on the kind discriminant.

### 6g: String()

Add a case in the `String()` method. Format should match Python's representation where reasonable.

## Step 7: Verify

Build the shared library for your host first; the Go tests load it. The first build compiles all of upstream and takes several minutes, so run it in the background:

```bash
MONTY_GO_FFI_SKIP_HEADER=1 scripts/build-go-ffi.sh <host-target-triple>   # e.g. aarch64-apple-darwin, aarch64-unknown-linux-gnu
```

Then run every check. Each Go module needs its own run:

```bash
cargo test -p monty-go-ffi --locked                # wire round-trip tests
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...
(cd examples && CGO_ENABLED=0 go run ./cmd/example) # prints 42
(cd otelmonty && CGO_ENABLED=0 go test ./...)
(cd cmd/shmonty && CGO_ENABLED=0 go test -buildvcs=false ./...)  # -buildvcs=false only needed in a git worktree
CGO_ENABLED=0 go test -run '^$' -fuzz FuzzLoadRunner -fuzztime 20s .
```

### Dump/load compatibility

`crates/monty-go-ffi/src/lib.rs` wraps upstream's own types (`StoredRunner`, `StoredLoadedRepl`, `StoredProgress`) and encodes them with `postcard`. Upstream changes to the serde layout of those types (renamed fields, `#[serde(rename)]`, flattened layouts) change gomonty's dump format even when no binding code changes. When the upstream range touches serialization:

- run the `FuzzLoadRunner` pass above, and make sure its seed corpus in `testdata/fuzz/FuzzLoadRunner` still loads
- say in the PR whether runners, REPLs and progress handles dumped by the previous release still load. If they don't, call it out as a breaking change

## Step 8: Branch, commit, push, and create PR

Commit **source and lockfile only**. Don't commit the shared library you built locally, the header, or `checksums.txt`: CI rebuilds the libraries for each platform, and only the `release-prep` workflow commits them.

```bash
git checkout -b <type>/<descriptive-branch-name>     # e.g. chore/bump-monty-pin-v1.0.1
git add Cargo.toml Cargo.lock crates/monty-go-ffi/src/wire.rs wire.go types.go types_test.go   # whatever you changed
git commit -m "Bump upstream Monty pin to <tag>"
git push -u origin <branch>
gh pr create --title "..." --body "..."   # include the Step 7 results and the dump/load note
```

`verify.yml` runs only when the PR is opened. After later pushes, recheck with `gh workflow run verify.yml --ref <branch>`.

## Step 9: Release

Follow `RELEASING.md` after the PR merges. It has two steps:

1. `make release` dispatches `release-prep.yml`, which rebuilds the shared libraries for every platform, regenerates the header and checksums, and opens a `release-prep/vX.Y.Z` PR. Pass `VERSION=vX.Y.Z` for a non-patch bump.
2. After that PR merges, `make publish-release VERSION=vX.Y.Z` tags `main`, creates the GitHub release, and warms the Go module proxy.

The shared libraries must be committed in the tagged tree, because `go get` fetches the tagged source and not GitHub release assets.

## Common pitfalls

- **Truncated SHA**: Always use the full 40-char commit SHA in `Cargo.toml`. The GitHub API list endpoint returns truncated SHAs — use `gh api repos/pydantic/monty/commits/<short-sha> --jq .sha` to get the full one.
- **Duplicate helpers**: Before adding `is_zero_*` functions in `wire.rs`, check if one already exists — the compiler will reject duplicates.
- **Wire constant ordering**: Go uses `iota` so constants must be in the same order as the Rust numeric values. Never renumber existing constants.
- **WireValue field reuse**: The `TimeZone` type reuses the `days` wire field for `offset_seconds` and `timezone_name` for `name`. This is intentional to keep the struct flat. When adding new types, check if existing fields can serve double duty before adding new ones.
- **Optional vs zero**: Rust `Option<T>` maps to Go `*T` (pointer). A zero value and an absent value are different — use pointer types for fields where `None` carries distinct meaning from the zero value.
