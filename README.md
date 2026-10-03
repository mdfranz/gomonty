# gomonty

A cgo-free Go binding for [Monty](https://github.com/pydantic/monty), the sandboxed Python interpreter from Pydantic written in Rust. Run untrusted or model-generated Python from Go, with the host in control of every file, function and resource the code can touch.

- Go module: `github.com/mdfranz/gomonty` (package `monty`)
- API docs: https://pkg.go.dev/github.com/mdfranz/gomonty
- Status: experimental

## Origin and maintenance

gomonty was created by Eric Hauser as [ewhauser/gomonty](https://github.com/ewhauser/gomonty); all work through `v0.0.14` (March 2026) is theirs, including the purego loading, the Rust C ABI crate and the original Go API. The upstream repository's last commit is dated 2026-03-29.

I forked it on 2026-09-05 and have maintained this fork since. The motive was practical: [sparktea](./docs/case-study-sparktea.md), my Go chat TUI, wanted Monty-backed code mode on a current Monty release, and a fork gave me somewhere to carry fixes without waiting on upstream. Releases `v0.0.15` onward come from here, and the module path is now `github.com/mdfranz/gomonty`. As of 2026-10-03 this fork is 64 commits ahead of `ewhauser/gomonty` and has not merged anything back, so treat it as a divergent fork rather than a mirror.

Changes since the fork (releases `v0.0.15` to `v0.0.17`, plus the docs on the unreleased `docs/update-documentation` branch):

- **Newer Monty.** Refreshed the pinned upstream Monty several times, most recently to v1.0.1, and updated the wire format and Go types for the upstream changes (see [docs/UPSTREAM-REFRESH-PLAN.md](./docs/UPSTREAM-REFRESH-PLAN.md) and the `upstream-refresh` skill).
- **Telemetry.** An opt-in `TelemetryHandler` API with a `log/slog` adapter, plus `otelmonty`, a separate module that exports OpenTelemetry traces.
- **`shmonty`.** An interactive REPL and script runner, with history, rewind, a telemetry pane and OTLP export.
- **Fixes.** `Value.String()` now renders containers, and `print()` results show `None` correctly.
- **CI and release.** `--locked` Rust builds, a faster `verify` workflow that runs when a PR is opened, optional musl CI, and a release flow that publishes the committed release tree.
- **Docs.** Architecture, Monty context, a Go + Rust implementation guide and a learning path under [`docs/`](./docs).

**Breaking changes from `v0.0.14`**, both driven by upstream Monty:

- `Dataclass`, `DataclassValue` and `Value.Dataclass()` were replaced by `ClassInstance` and its constructors.
- `ResourceLimits.MaxAllocations` was removed, and `MaxRecursionDepth` and `MaxSuspensions` were added.

I know of no other breaking changes. The module path differs from the original (`github.com/ewhauser/gomonty`), so switching between the two means changing import paths.

## Install

```bash
go get github.com/mdfranz/gomonty@latest
```

Requires Go 1.25+ and `CGO_ENABLED=0`. Tagged versions ship the native shared libraries for darwin/arm64, linux/amd64, linux/arm64 and windows/amd64. On Alpine or other musl Linux, build with `-tags musl`.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	monty "github.com/mdfranz/gomonty"
)

func main() {
	runner, err := monty.New("40 + 2", monty.CompileOptions{ScriptName: "example.py"})
	if err != nil {
		log.Fatal(err)
	}

	value, err := runner.Run(context.Background(), monty.RunOptions{})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(value.Raw()) // 42
}
```

Run it from a checkout with `cd examples && CGO_ENABLED=0 go run ./cmd/example`. If you are using a branch or unreleased commit, build the native library for your platform first (see [docs/contributing.md](./docs/contributing.md)).

## Core features

| Feature | What it gives you | Where to look |
| --- | --- | --- |
| External functions | Python code calls Go functions you register; execution pauses while Go runs. | `RunOptions.Functions`, [`go/README.md`](./go/README.md) |
| Virtual filesystem | `pathlib` and `os.getenv` calls are served by a Go-owned filesystem and environment, never the host disk. | [`vfs/`](./vfs) |
| Async results | Host calls can return `Pending(waiter)` and resolve later. | `Pending`, `FutureSnapshot` |
| Pause/resume | Step through execution with `Start`, and serialize snapshots with `Dump`/`Load*`. | `Runner.Start`, `Snapshot` |
| REPL | Feed successive snippets into one persistent interpreter. | `NewRepl`, `Repl.FeedRun` |
| Type checking | Check code against stubs before running it. | `Runner.TypeCheck`, `CompileOptions.TypeCheck` |
| Resource limits | Bound duration, memory, recursion depth and host-call count. | `ResourceLimits` |
| Telemetry | Opt-in structured logs or OpenTelemetry spans for runs, callbacks and waits. | `SlogHandler`, [`otelmonty/`](./otelmonty) |
| `shmonty` | Interactive terminal REPL and script runner built on the library. | [`cmd/shmonty/`](./cmd/shmonty) |

## What gomonty adds on top of upstream

Upstream Monty is a Rust library. gomonty is the Go layer around it:

- **No cgo.** The native library is embedded, extracted to the user cache directory and loaded at runtime with [purego](https://github.com/ebitengine/purego), so Go cross-compilation and `CGO_ENABLED=0` keep working.
- **A C ABI and MessagePack wire format** (`crates/monty-go-ffi`) that map upstream values, errors and snapshots to idiomatic Go types.
- **Go-native host integration:** typed external-function callbacks, a pluggable `vfs.FileSystem`, and `print()` capture.
- **Observability:** a telemetry interface with a `log/slog` handler, plus a separate `otelmonty` module that exports OpenTelemetry traces to any OTLP backend.
- **Tooling:** the `shmonty` REPL, fuzz targets, benchmarks against raw Monty, and release and upstream-refresh automation.

## Learn more

- [docs/architecture.md](./docs/architecture.md): how the Go, wire, FFI and Rust layers fit together
- [docs/GO-RUST-GUIDE.md](./docs/GO-RUST-GUIDE.md): the Go and Rust sides of the implementation, concept by concept
- [docs/monty-context.md](./docs/monty-context.md): what Monty is and how gomonty relates to it
- [docs/learning-path.md](./docs/learning-path.md): staged exercises for building Go tools around Monty
- [docs/case-study-sparktea.md](./docs/case-study-sparktea.md): a real consumer
- [`go/README.md`](./go/README.md): detailed API guide (values, errors, async, pause/resume)
- [docs/ci.md](./docs/ci.md): CI and release workflows
- [docs/contributing.md](./docs/contributing.md): building, testing, benchmarks, fuzzing, upstream overrides
- [docs/UPSTREAM-REFRESH-PLAN.md](./docs/UPSTREAM-REFRESH-PLAN.md): record of a completed upstream bump
- [`RELEASING.md`](./RELEASING.md): release flow and bumping the upstream pin
- [`gomonty-olly.md`](./gomonty-olly.md): telemetry design
