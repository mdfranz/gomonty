---
name: review-ffi
description: Review changes that touch the Go/Rust FFI boundary in gomonty. Use when reviewing or writing changes to crates/monty-go-ffi (lib.rs, wire.rs), internal/ffi, wire.go, or the progress/snapshot handling in runner.go and dispatch.go, or when asked to review FFI, wire format, memory ownership, or panic safety.
---

# Reviewing FFI changes

The boundary has no compiler check: Go binds Rust symbols by name at runtime (purego) and the two wire structs are matched by tag, not by type. Review for the things neither compiler can catch. Background: `docs/GO-RUST-GUIDE.md`.

## Checklist

### Rust (`crates/monty-go-ffi`)

- **Panic guard.** Every `pub extern "C"` function wraps its body in the matching `catch_*_result` helper (`lib.rs`). A panic escaping `extern "C"` aborts the whole Go process. New exports must be guarded; `grep -n 'pub extern "C"' crates/monty-go-ffi/src/lib.rs` and check each.
- **Ownership pairs.** Anything handed to Go (`Box::into_raw`, `MontyGoBytes::from_vec`) has a matching `*_free`, and Go calls it exactly once. A new handle type needs a free function, an `Option::take()` style guard against reuse, and a Go `Close` that is idempotent.
- **Null and length checks.** Raw pointers are null-checked before dereferencing; `slice_from_raw` is used for `(ptr, len)` pairs. Each `unsafe` block has a `// SAFETY:` comment (the workspace lints require it).
- **Errors are values.** Failures return an `FfiError`, never `unwrap()`/`expect()` on data that came from Go.

### Wire format (`wire.rs` and `wire.go`)

- **Constants never renumbered.** `WIRE_VALUE_*`, `WIRE_CALL_RESULT_*`, `WIRE_LOOKUP_RESULT_*` and `WIRE_PROGRESS_*` values must equal the Go `iota` position. New tags go at the end of both lists; retired ones stay as unused slots.
- **Struct parity.** Every field added to a Rust `Wire*` struct has the same name in the Go struct's `msgpack:"..."` tag, with `#[serde(default)]` on the Rust side so older payloads still decode. Optional means `Option<T>` in Rust and a pointer in Go.
- **Both directions.** A new value kind needs `from_monty` and `into_monty` in Rust and the encode and decode paths in Go, plus a round-trip test (`wire_test.go`, `wire.rs` tests).
- **Byte values** cross as MessagePack binary, not strings (issue #42 was this).

### Go (`internal/ffi`, `runner.go`, `dispatch.go`)

- **Keep Go memory alive** across the call: `runtime.KeepAlive` on slices whose pointer was passed to Rust.
- **Copy then free** Rust-owned bytes via `takeBytes`; never retain a slice that points at Rust memory.
- **Handle state.** Mutating operations go through `borrow()`/`take()` so concurrent use returns `ErrConcurrentUse` and use-after-close returns `ErrClosed`.
- **Every progress kind is handled** where `progressFromResult` and `dispatchLoop` switch on it.

### Persistence

Dumps are upstream types serialized by the FFI. Changes to wrapped upstream types, or an upstream bump, must keep `persistence_test.go` and `FuzzLoadRunner` passing. If old dumps stop loading, say so in the PR; do not hide it.

## Verify

```bash
MONTY_GO_FFI_SKIP_HEADER=1 scripts/build-go-ffi.sh <host target triple>
cargo test -p monty-go-ffi --locked
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go test -run '^$' -fuzz FuzzLoadRunner -fuzztime=30s .
```

Rebuild the host library first: Go tests run against it, so a stale library makes a wire change look fine. Do not commit the rebuilt library or header; see `AGENTS.md`.

## Known gaps (do not treat as new findings)

Issue #33 tracks 7 exports without panic guards and the unenforced clippy set. Report a new unguarded export, but link #33 rather than re-filing it.
