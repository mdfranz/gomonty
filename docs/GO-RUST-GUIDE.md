# Go + Rust Implementation Guide

gomonty is a Go library whose engine is Rust. This guide walks through the codebase concept by concept and shows how each idea appears on both sides of the boundary, so you can read the Go wrapper and the Rust crate as one design. It assumes you know one of the two languages. For the layering, start with [architecture.md](./architecture.md).

Code excerpts are from this repo; paths link to the source.

## Map: concept → Go side → Rust side

| Concept | Go | Rust |
| --- | --- | --- |
| [Crossing the boundary](#1-crossing-the-boundary) | `purego.RegisterLibFunc` in [`internal/ffi/ffi.go`](../internal/ffi/ffi.go) | `extern "C"` functions in [`crates/monty-go-ffi/src/lib.rs`](../crates/monty-go-ffi/src/lib.rs) |
| [Loading native code](#2-loading-the-native-library) | `go:embed`, build tags, `Dlopen` | `crate-type = ["cdylib"]`, release profile |
| [Who owns memory](#3-who-owns-memory) | finalizers, `Close`, `takeBytes` | `Box::into_raw` / `Box::from_raw`, `Vec::from_raw_parts` |
| [Opaque handles](#4-opaque-handles) | `*ffi.Runner`, `*ffi.Progress` wrappers | `MontyGoRunner`, `MontyGoProgress` structs |
| [Data on the wire](#5-data-on-the-wire) | `wireValue` struct, `msgpack` tags | `WireValue` struct, `serde` attributes |
| [Constants that must match](#6-constants-that-must-match) | `iota` blocks | `pub const … : u8` |
| [Pause and resume](#7-pause-and-resume) | `dispatchLoop`, type switch on `Progress` | `enum StoredProgress`, `RunProgress` |
| [Errors and panics](#8-errors-and-panics) | `error` values, typed errors | `FfiError` enum, `catch_unwind` |
| [Concurrency guards](#9-concurrency-guards) | `sync.Mutex.TryLock`, `ErrConcurrentUse` | `&mut` exclusive borrows, `Option::take` |
| [Callbacks and capability](#10-callbacks-and-capability) | `ExternalFunction`, `vfs.Handler` | suspend points inside the VM |
| [Build and test](#11-build-and-test) | `go test`, fuzz targets | `cargo test`, cbindgen |

---

## 1. Crossing the boundary

Go cannot call Rust directly; it calls C-ABI functions. The Rust crate exports them with `extern "C"` and a stable symbol name. The Go side looks the symbols up at runtime and binds them to Go function variables.

**Rust** (`lib.rs`): an exported function with raw pointers and lengths instead of Go/Rust types.

```rust
#[unsafe(no_mangle)]
pub extern "C" fn monty_go_runner_start(
    runner: *const MontyGoRunner,
    options_ptr: *const u8,
    options_len: usize,
    out: *mut MontyGoOpResult,
) { /* ... */ }
```

- `#[unsafe(no_mangle)]` keeps the symbol name exactly `monty_go_runner_start`.
- `extern "C"` selects the C calling convention.
- Results come back through an `out` pointer the caller owns, because returning large structs by value is less portable across FFI.

**Go** (`ffi.go`): a table of symbol names and destination function variables, bound with purego.

```go
runnerStart func(runner *cRunner, optionsPtr *byte, optionsLen uintptr, out *cOpResult)
// ...
{"monty_go_runner_start", &a.runnerStart},
// ...
purego.RegisterLibFunc(dst, handle, name)
```

`RegisterLibFunc` inspects the Go function's signature and builds the call trampoline, with no cgo and no C compiler. A Go `*byte` plus `uintptr` pair lines up with Rust's `*const u8` plus `usize`.

The C declarations are generated for reference from the Rust side by cbindgen (`crates/monty-go-ffi/cbindgen.toml` → `internal/ffi/include/monty_go_ffi.h`). Go does not use the header; purego does not need it.

If a name or signature drifts between the two sides, there is no compiler to catch it. The Go test suite and `cargo test` together are what guard it.

## 2. Loading the native library

**Rust:** the crate builds as a `cdylib`, a shared library with only the C ABI exported (`crates/monty-go-ffi/Cargo.toml`). The workspace release profile is tuned for size and speed (`lto = "fat"`, `codegen-units = 1`, `strip = true`).

**Go:** one file per platform embeds the matching library, selected by build constraints:

```go
//go:build linux && amd64 && !musl

//go:embed lib/linux_amd64/libmonty_go_ffi.so
var embeddedLibs embed.FS
```

On first use `ensureLoaded` (`sync.Once`) extracts the library to the user cache directory and opens it:

```go
func loadLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}
```

Go-side idioms to notice: build tags (`!musl`) choose between glibc and musl builds, `sync.Once` makes loading safe from many goroutines, and a load failure is stored and returned as a normal error rather than panicking. Windows uses a separate `load_library_windows.go`.

## 3. Who owns memory

Two allocators and two garbage-collection models meet here, so ownership rules are explicit:

- **Rust allocates, Rust frees.** Anything Rust hands out, such as a byte buffer or a handle, must go back to Rust to be freed. Go's GC knows nothing about it.
- **Go memory is only borrowed.** Rust receives `(ptr, len)` for the duration of one call and must not keep it.

**Rust returns bytes as a leaked `Vec`:**

```rust
#[repr(C)]
pub struct MontyGoBytes { pub ptr: *mut u8, pub len: usize }
```

and reclaims them when Go calls back:

```rust
pub extern "C" fn monty_go_bytes_free(ptr: *mut u8, len: usize) {
    if !ptr.is_null() && len > 0 {
        // SAFETY: ptr/len were allocated by `MontyGoBytes::from_vec`
        unsafe { let _ = Vec::from_raw_parts(ptr, len, len); }
    }
}
```

**Go copies, then frees:**

```go
func takeBytes(bytes cBytes) []byte {
	if bytes.ptr == nil || bytes.len == 0 { return nil }
	defer api.bytesFree(bytes.ptr, bytes.len)
	view := unsafe.Slice(bytes.ptr, int(bytes.len))
	return append([]byte(nil), view...)
}
```

`append([]byte(nil), view...)` makes a Go-owned copy before the deferred free runs. After that, no Go slice points at Rust memory.

**Going the other way**, Go passes a pointer into its own slice and keeps it reachable until the call returns:

```go
optionsPtr, optionsLen := byteArgs(options)
api.runnerStart(r.ptr, optionsPtr, optionsLen, &result)
runtime.KeepAlive(options)
```

`runtime.KeepAlive` stops the GC from collecting `options` while Rust is still reading it.

## 4. Opaque handles

A compiled program, a REPL session and a paused execution are Rust structures that Go never inspects. Rust boxes them and gives Go a raw pointer; Go wraps it in a struct and treats it as a token.

**Rust:**

```rust
pub struct MontyGoRunner {
    runner: MontyRun,
    script_name: String,
    input_names: Vec<String>,
}
```

Creation uses `Box::into_raw(Box::new(...))`, which moves the value to the heap and forgets the owner. Destruction is the inverse, and a null check makes double-free-by-null harmless:

```rust
pub extern "C" fn monty_go_runner_free(runner: *mut MontyGoRunner) {
    if !runner.is_null() {
        // SAFETY: pointer was created by `Box::into_raw`
        unsafe { drop(Box::from_raw(runner)) };
    }
}
```

**Go:** the pointer type is an empty struct (`type cRunner struct{}`), so Go can hold it but cannot dereference anything meaningful. The wrapper adds cleanup:

```go
runner := &Runner{ptr: ptr}
runtime.SetFinalizer(runner, (*Runner).Close)
```

`Close` is idempotent (`r.ptr = nil` after freeing), so calling it explicitly and then letting the finalizer run is safe. Prefer explicit `Close`; the finalizer is a safety net, not a schedule.

## 5. Data on the wire

Handles carry state; *values* (Python ints, lists, dicts, exceptions, dates) cross as MessagePack bytes.

**Rust** defines one wide struct and uses `serde` attributes to keep the encoding small:

```rust
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
pub struct WireValue {
    pub kind: u8,
    #[serde(default, skip_serializing_if = "is_zero_i64", rename = "int")]
    pub int_value: i64,
    #[serde(default, skip_serializing_if = "String::is_empty", rename = "string")]
    pub string_value: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub items: Vec<WireValue>,
    // ...
}
```

**Go** mirrors it with struct tags:

```go
type wireValue struct {
	Kind   uint8       `msgpack:"kind"`
	Int    int64       `msgpack:"int,omitempty"`
	String string      `msgpack:"string,omitempty"`
	Items  []wireValue `msgpack:"items,omitempty"`
	// ...
}
```

The equivalences: `skip_serializing_if` ↔ `omitempty`, `rename` ↔ the tag name, `Option<String>` ↔ `*string`, `Vec<T>` ↔ `[]T`. Optional fields are pointers in Go because the zero value of a `string` is indistinguishable from "absent".

Why one struct with a `kind` tag instead of a tagged union? It maps directly to Go (which has no sum types), keeps the schema flat, and lets either side add fields without breaking older payloads: `#[serde(default)]` on the Rust side tolerates missing fields.

Rust encodes with `rmp_serde::to_vec_named` and decodes with `rmp_serde::from_slice` (`encode_wire`/`decode_wire`). "Named" means field names, not positions, are encoded, which is what makes the two structs compatible by tag instead of by order.

Conversion to and from the interpreter's own type is explicit: `WireValue::from_monty(&MontyObject)` and `WireValue::into_monty(self) -> Result<MontyObject, String>`. Go's public `Value` type (`types.go`) is a separate, friendlier representation with constructors (`Int`, `String`, `List`, ...) so users never touch `wireValue`.

## 6. Constants that must match

Tags like "this value is an int" are bare integers on the wire. Both sides define them by hand.

**Rust:**

```rust
pub const WIRE_VALUE_NONE: u8 = 0;
pub const WIRE_VALUE_BOOL: u8 = 2;
pub const WIRE_VALUE_INT: u8 = 3;
```

**Go:**

```go
const (
	wireValueNone uint8 = iota
	wireValueEllipsis
	wireValueBool
	wireValueInt
	// ...
)
```

`iota` assigns 0, 1, 2, … by position, so **the order of the Go block is the numbering**. Inserting a constant in the middle silently shifts every later tag and breaks the Rust side. That is why the rule in [`AGENTS.md`](../AGENTS.md) says never to renumber, and why a retired value like `WIRE_VALUE_DATACLASS = 16` stays in place as an unused slot (with a comment on both sides) instead of being deleted.

Rust gives explicit numbers; Go gives implicit ones. When the two disagree there is no compile error, only wrong behavior at runtime. Add new tags at the end of both lists.

## 7. Pause and resume

Monty runs until it needs something from the host, then returns a *progress* value. The two languages model this with their native idioms.

**Rust** uses an enum (a sum type) holding whichever state the interpreter paused in:

```rust
enum StoredProgress {
    Run  { progress: RunProgress,  script_name: String },
    Repl { progress: ReplProgress, script_name: String },
}
```

`monty_go_progress_resume_call` checks the variant with `matches!`, then *moves* the state out with `Option::take()` so the handle cannot be resumed twice:

```rust
let Some(inner) = progress.inner.take() else { /* error: no longer available */ };
```

Resuming consumes the old state and produces a new one. Rust's ownership rules make "use after resume" unrepresentable; the `Option` plus `take()` carries the same guarantee across the C ABI.

**Go** has no sum types, so the progress is an interface with a marker method, and the loop is a type switch (`dispatch.go`):

```go
switch current := progress.(type) {
case *Complete:
	return current.Output, nil, timing
case *Snapshot:          // external or OS call
	// run the Go handler, then resumeCall(...)
case *NameLookupSnapshot:
case *FutureSnapshot:
}
```

The four Go types correspond to the four `WIRE_PROGRESS_*` tags. `progressFromResult` (`runner.go`) decodes the payload's `kind` into the right Go type.

Note the Go side also keeps an invariant: after `resumeCall`, the old Go snapshot's handle has been taken (`progressState.take`), so reusing it returns `ErrClosed` instead of reaching Rust.

## 8. Errors and panics

**In Rust**, failures are an enum:

```rust
enum FfiError {
    Exception(MontyException),   // Python-level error from the guest
    Typing(TypingFailure),       // static type-check failure
    Api(String),                 // misuse or internal failure
}
```

A Rust panic must never unwind across the C boundary (that is undefined behavior). Every exported function therefore wraps its body:

```rust
match catch_unwind(AssertUnwindSafe(f)) {
    Ok(result) => result,
    Err(payload) => MontyGoRunnerResult::err(ffi_panic_error(payload)),
}
```

so a panic becomes an ordinary `FfiError::Api("monty-go ffi panicked: …")` that Go receives as an error handle.

**Across the boundary**, errors are handles too. Go asks for a JSON summary (`monty_go_error_json`) or rendered text (`monty_go_error_display`), then maps it to Go error types (`SyntaxError`, `RuntimeError`, `TypingError` in `errors.go`). Go callers use the normal `error` interface and `errors.As`.

Idiom comparison: Rust returns `Result<T, E>` and uses `?`; Go returns `(T, error)` and checks `err != nil`. The FFI layer flattens both into "value or error handle" structs (`MontyGoOpResult` has both `progress` and `error` fields, one of them null).

## 9. Concurrency guards

A runner or snapshot must not be used by two goroutines at once, and Rust's `&mut` rules only protect Rust code, not the Go callers above it. Go enforces it with a non-blocking lock:

```go
func (s *runnerState) borrow() (*ffi.Runner, func(), error) {
	if s == nil || !s.mu.TryLock() {
		return nil, nil, ErrConcurrentUse
	}
	if s.handle == nil {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	return s.handle, s.mu.Unlock, nil
}
```

`TryLock` fails fast with `ErrConcurrentUse` instead of waiting, because concurrent use is a bug to report, not a contention to smooth over. The returned `release` function is called with `defer`, a Go idiom comparable to Rust's RAII `Drop`.

The REPL needs more care: its state must survive an execution error, so `takeForExecution` removes the handle, marks `inFlight`, and `restore` puts the recovered REPL back when Rust returns it. This mirrors Rust's move-out/move-back of owned state.

## 10. Callbacks and capability

The sandboxed program cannot read a file or call a function by itself. In Rust that capability is absent from the VM; in Go it is added back one explicit piece at a time:

```go
runner.Run(ctx, monty.RunOptions{
	Functions: map[string]monty.ExternalFunction{ "host_add": host_add },
	OS:        vfs.Handler(fs, env),
	Limits:    &monty.ResourceLimits{MaxDuration: 2 * time.Second},
})
```

Because Monty *pauses* instead of calling out, there is no callback function pointer crossing the FFI boundary and no Go-calling-Rust-calling-Go re-entrancy. Go always drives: it calls `start`, gets a snapshot, runs the handler in ordinary Go (with `context`, goroutines and its own error handling), and calls `resume`. That is the key simplification of this design, and it keeps the FFI surface to plain data and opaque handles.

`ExternalFunction` is a plain function type (`func(context.Context, Call) (Result, error)`). `Result` is a small tagged struct (`Return`, `Raise`, `Pending`), the Go stand-in for the Rust-side `WireCallResult` / `ExtFunctionResult` enum.

## 11. Build and test

| Task | Go | Rust |
| --- | --- | --- |
| Unit tests | `CGO_ENABLED=0 go test ./...` (needs the built library) | `cargo test -p monty-go-ffi --locked` |
| Fuzz | `go test -fuzz FuzzCompileAndRun` etc. | not used |
| Format | `gofmt` | `cargo fmt` |
| Build the shared library | n/a | `scripts/build-go-ffi.sh <target>` |
| Dependencies | `go.mod` (`purego`, `msgpack`) | `Cargo.toml` (`monty` pinned by git rev, `rmp-serde`, `postcard`, `serde`) |

Two testing consequences of the design:

- Go tests exercise the real native library, so they double as integration tests of the wire format. Rust unit tests (in `wire.rs`) cover the wire conversions in isolation.
- Because the ABI has no compiler check, a change to a wire struct needs a matching edit on both sides and a rebuilt library before Go tests mean anything. CI rebuilds the libraries per platform; locally, rebuild with `scripts/build-go-ffi.sh` first (see [contributing.md](./contributing.md)).

Snapshots use a third encoding, `postcard`, internal to Rust. Go treats a dumped snapshot as opaque bytes (`Dump`/`LoadSnapshot`) and never decodes it, so the wire format and the persistence format can evolve separately.

## Reading order

1. [`crates/monty-go-ffi/src/lib.rs`](../crates/monty-go-ffi/src/lib.rs): module comment, then `MontyGoRunner`, `catch_*`, `monty_go_runner_new`, `monty_go_runner_start`.
2. [`internal/ffi/ffi.go`](../internal/ffi/ffi.go): `nativeAPI`, `ensureLoaded`, `takeBytes`, `Runner.Start`.
3. [`crates/monty-go-ffi/src/wire.rs`](../crates/monty-go-ffi/src/wire.rs) next to [`wire.go`](../wire.go).
4. [`runner.go`](../runner.go) (`progressFromResult`, `borrow`) and [`dispatch.go`](../dispatch.go) (`dispatchLoop`).
5. [`examples/cmd/codemode`](../examples/cmd/codemode) to see the whole path from the user's side.
