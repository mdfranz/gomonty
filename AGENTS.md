# AGENTS.md

Guidance for coding agents working in this repo. `CLAUDE.md` imports this file.

gomonty is the cgo-free Go binding for [pydantic/monty](https://github.com/pydantic/monty), a sandboxed Python interpreter written in Rust. The Rust crate `crates/monty-go-ffi` exposes a C ABI, and Go loads the resulting shared library at runtime with `purego`.

## Layout

There are four Go modules, connected with `replace` directives. Run `go test` in each one separately:

| Module | Path |
| --- | --- |
| library (`monty`, `vfs/`, `internal/ffi/`) | `.` |
| examples | `examples/` |
| OpenTelemetry bridge | `otelmonty/` |
| REPL (`shmonty`) | `cmd/shmonty/` |

- Architecture, upstream context and a learning path are in `docs/` (start with `docs/architecture.md`).
- Values cross the boundary as MessagePack. Changes flow top-down: upstream `MontyObject` → `crates/monty-go-ffi/src/wire.rs` → `wire.go` → `types.go`. The `upstream-refresh` skill covers this in detail.
- The design of the telemetry API and `otelmonty` is in `gomonty-olly.md`.

## Building and testing

The Go tests need the native shared library for your platform, so build it first:

```bash
MONTY_GO_FFI_SKIP_HEADER=1 scripts/build-go-ffi.sh aarch64-unknown-linux-gnu   # or your host's target triple
CGO_ENABLED=0 go test ./...
cargo test -p monty-go-ffi --locked
```

- The first build compiles all of upstream Monty and takes several minutes; run it in the background.
- In a `git worktree`, building `cmd/shmonty` fails with "error obtaining VCS status". Pass `-buildvcs=false`.
- Alpine/musl needs the `musl` Go build tag.

## Rules

- **Don't commit locally built shared libraries** (`internal/ffi/lib/**`), the header, or `internal/ffi/checksums.txt` in feature PRs. CI rebuilds the libraries for each platform, and only the `release-prep` workflow commits them. See `RELEASING.md`.
- The upstream pin in `Cargo.toml` (`monty`, `monty_type_checking`, `monty_types`) must use one full 40-character commit hash for all three entries.
- Never renumber `WIRE_VALUE_*` constants; Go uses `iota`, so their order must match the Rust values.
- `verify.yml` runs only when a PR is first opened. To recheck later pushes, run `gh workflow run verify.yml --ref <branch>`.
- Releases: `make release` opens a release-prep PR. After it merges, run `make publish-release VERSION=vX.Y.Z`.

## Skills

Project skills live in `.agents/skills/`. `.claude/skills` is a symlink to that directory, so Claude Code loads them.
