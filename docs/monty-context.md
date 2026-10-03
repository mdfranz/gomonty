# gomonty in the context of Monty

## What Monty is

[Monty](https://github.com/pydantic/monty) is a Python interpreter written in Rust by the Pydantic team, built for running code written by an LLM safely. It implements a subset of Python, starts in microseconds, and has no ambient access to files, network, environment or the host process. Anything beyond pure computation has to be granted by the host explicitly.

That is the model gomonty exposes in Go: the sandboxed code is the guest, and the Go program decides which functions, files and limits the guest gets.

## Upstream crates gomonty uses

| Crate | Role | Used by gomonty |
| --- | --- | --- |
| `monty` | The interpreter: parser, bytecode VM, values, snapshots, resource limits. | Yes, directly and in-process. |
| `monty-type-checking` | Static type checking of sandboxed code. | Yes, behind `Runner.TypeCheck`. |
| `monty-types` | Shared type definitions. | Yes. |
| `monty-pool` | An async worker pool with its own telemetry feature. | No. gomonty instruments at the Go boundary instead (see [`gomonty-olly.md`](../gomonty-olly.md)). |

All three crates the FFI uses are pinned to one full commit hash in the root [`Cargo.toml`](../Cargo.toml). The pin moves only through the upstream-refresh process in [`RELEASING.md`](../RELEASING.md).

## Upstream concepts and their Go names

| Upstream concept | Go API |
| --- | --- |
| `MontyObject` (a Python value crossing the boundary) | `monty.Value`, with constructors such as `Int`, `String`, `List`, `DictValue` and `Value.Raw()` |
| External function (a name the guest calls that the host implements) | `RunOptions.Functions` / `ExternalFunction` |
| OS call (`Path.read_text`, `os.getenv`, ...) | `RunOptions.OS` / `OSHandler`, usually `vfs.Handler` |
| Paused execution state | `Snapshot`, `NameLookupSnapshot`, `FutureSnapshot` |
| Serialized snapshot | `Dump()` and `LoadSnapshot` / `LoadRunner` |
| Resource tracker limits | `ResourceLimits` |
| Persistent REPL session | `Repl`, `FeedRun`, `FeedStart` |
| Type checker | `Runner.TypeCheck`, `CompileOptions.TypeCheck` |

## Why a "pause and ask the host" design

Because the interpreter returns control at every host interaction instead of calling out, a host can:

- run guest code with no threads or callbacks crossing the FFI boundary,
- store a paused program and resume it later or on another machine,
- enforce limits (including a cap on host-call count) around the pause points,
- resolve results asynchronously.

This is also what makes "code mode" agents practical: the model writes one program that calls many tools, rather than making one tool call per turn. See [learning-path.md](./learning-path.md) and the [sparktea case study](./case-study-sparktea.md).

## Staying in sync with upstream

1. Upstream changes land in the pinned crates.
2. The Rust crate and `wire.rs` are updated to match, then `wire.go` and `types.go`.
3. Release automation rebuilds and commits the native libraries.

The `upstream-refresh` skill automates the checklist; see [architecture.md](./architecture.md#adding-or-changing-a-value-type) for the layering.

## Not in scope

gomonty does not replace CPython: only the subset of Python that Monty supports works, and third-party packages cannot be imported. Check upstream's documentation for the current language coverage.
