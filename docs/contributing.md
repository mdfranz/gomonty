# Contributing

Developer notes for building, testing, benchmarking and releasing gomonty. For the code layout see [architecture.md](./architecture.md); for agent/contributor rules see [`AGENTS.md`](../AGENTS.md).

## Testing each module

There are five Go modules (library, `examples/`, `otelmonty/`, `cmd/shmonty/`, `remote/`). Run `go test` in each one separately (`remote/` uses an in-process fake worker and does not require the native library; the others do after building it for your host):

```bash
MONTY_GO_FFI_SKIP_HEADER=1 scripts/build-go-ffi.sh <host target triple>
CGO_ENABLED=0 go test ./...
(cd remote && CGO_ENABLED=0 go test ./...)
cargo test -p monty-go-ffi --locked
```

The first build compiles all of upstream Monty and takes several minutes. Do not commit locally built shared libraries; see [`RELEASING.md`](../RELEASING.md).

## Testing the remote client against a real worker

For connecting to remote workers and distributed tracing, see [remote.md](./remote.md). The `remote/` tests normally run against an in-process fake worker. `remote/realworker_test.go` runs the same kinds of scenarios (host functions, OS calls, name lookups, futures, large values, cancellation) against a real Monty worker. Run it with:

```bash
make remote-realworker
```

The script builds `monty` from the upstream commit pinned in `Cargo.toml` (cached under `~/.cache/gomonty`; the first build takes several minutes), starts upstream's `scripts/grpc_relay.py` from that commit on a localhost port, runs the tests and stops the relay. It needs `git`, `cargo` and `uv`. Run it after bumping the upstream pin.

The tests skip unless `MONTY_REMOTE_ENDPOINT` is set, so `go test ./...` is unaffected. To point them at another worker, set `MONTY_REMOTE_ENDPOINT` (and `MONTY_REMOTE_TOKEN` for a TLS endpoint, or `MONTY_REMOTE_INSECURE=1` for a plain local one) and run `go test -run RealWorker ./remote`. The local relay covers protocol handling only: it has no TLS, authentication or capacity limits.

## Build Notes

The Go package is cgo-free. It uses `purego` to load a bundled shared library for the current target from `internal/ffi/lib/<target>`.

On first use, the loader extracts the embedded shared library to `os.UserCacheDir()` with an `os.TempDir()` fallback, then opens it with the platform loader.

Default Linux builds target the GNU/glibc shared libraries. Alpine and other musl-based Linux builds must opt into the musl family with the `musl` Go build tag.

On PRs, the `verify` workflow runs `CGO_ENABLED=0` Go tests on native Linux (amd64 and arm64), plus the Rust FFI crate's unit tests, `clippy`, and `gofmt`/`cargo fmt` checks. Weekly scheduled runs and manual `workflow_dispatch` runs cover all platforms, including `darwin-arm64`, Windows (`TestPlatformSmoke`), and Alpine musl (`TestPlatformSmoke` in a container); those extra platform checks can also be enabled on PRs with `RUN_MACOS_CI`, `RUN_WINDOWS_CI`, or `RUN_MUSL_CI`. `release-prep` smoke-tests all six built platform artifacts with `TestPlatformSmoke` before opening a release PR.

To build or refresh the shared library for the current host:

```bash
scripts/build-go-ffi.sh aarch64-apple-darwin
CGO_ENABLED=0 go test ./...
```

Requirements:

- Go 1.27.4+
- Rust toolchain
- Python available on `PATH`, or `PYO3_PYTHON` set explicitly
- `cbindgen` only when regenerating `internal/ffi/include/monty_go_ffi.h`

For repeat builds where the checked-in header does not need to change, set `MONTY_GO_FFI_SKIP_HEADER=1`.

For Alpine or another musl-based Linux environment:

```bash
scripts/build-go-ffi.sh x86_64-unknown-linux-musl
go test -tags musl ./...
```

## Benchmarks

The Go benchmark suite mirrors the current upstream Monty benchmark cases so
the two projects exercise the same scripts and expected outputs. The shared
kitchen-sink workload is copied into [`testdata/bench_kitchen_sink.py`](../testdata/bench_kitchen_sink.py).

With a host shared library built, run the local Go-only benchmarks with:

```bash
CGO_ENABLED=0 go test -run '^$' -bench BenchmarkMonty -benchmem
```

This covers the parse-once/repeated-run benchmark cases plus
`BenchmarkMontyEndToEnd` for parse-and-run in the loop.

There are also Go-specific benchmark suites for wrapper overhead:

```bash
CGO_ENABLED=0 go test -run '^$' -bench BenchmarkMontyCallbacks -benchmem
CGO_ENABLED=0 go test -run '^$' -bench BenchmarkMontyDecompose -benchmem
```

These add:

- callback-heavy runs with repeated external function and OS handler calls
- low-level decomposition benchmarks for compile-only, dump/load, start-to-first-progress, name lookup, call resume, and pending resume paths

To capture CPU and allocation profiles for the representative hot paths, run:

```bash
scripts/profile-benchmarks.sh
```

By default the script writes profiles and `pprof -top` summaries to `/tmp/gomonty-bench-profiles` for:

- `BenchmarkMontyEndToEnd`
- `BenchmarkMonty/list_append_int`
- `BenchmarkMontyCallbacks/external_loop`

To compare `gomonty` against a local upstream Monty checkout on the same host,
run:

```bash
python3 scripts/compare-benchmarks.py --upstream ../monty
```

The comparison script:

- runs the Go benchmark suite and aggregates the median `ns/op` across three runs
- runs the upstream Criterion `__monty` benchmarks
- sets `PYO3_PYTHON` for the upstream run if the upstream checkout still expects a local `.venv/bin/python3`
- prints a Markdown table suitable for pasting back into this document

Current sample comparison from 2026-03-24 on `darwin/arm64` (`Apple M3 Max`),
measured from `gomonty` `dddae9616d8b-dirty` against upstream Monty
`982709bd52b1-dirty`:

| Case | gomonty | raw monty | Ratio |
| --- | ---: | ---: | ---: |
| `add_two` | `2.916 us` | `721 ns` | `4.04x` |
| `list_append` | `3.204 us` | `853 ns` | `3.76x` |
| `loop_mod_13` | `42.157 us` | `37.906 us` | `1.11x` |
| `kitchen_sink` | `7.942 us` | `4.035 us` | `1.97x` |
| `func_call_kwargs` | `3.501 us` | `1.045 us` | `3.35x` |
| `list_append_str` | `14.200 ms` | `14.557 ms` | `0.98x` |
| `list_append_int` | `4.855 ms` | `4.976 ms` | `0.98x` |
| `fib` | `20.547 ms` | `21.204 ms` | `0.97x` |
| `list_comp` | `32.750 us` | `29.786 us` | `1.10x` |
| `dict_comp` | `78.033 us` | `69.671 us` | `1.12x` |
| `empty_tuples` | `2.664 ms` | `2.794 ms` | `0.95x` |
| `pair_tuples` | `8.917 ms` | `9.111 ms` | `0.98x` |
| `end_to_end` | `5.240 us` | `1.891 us` | `2.77x` |

These numbers are host-specific. They compare the same benchmark scripts, but
the Go side uses `testing.B` while upstream uses Criterion.

## Fuzzing

The repo also includes Go fuzz targets for:

- `FuzzValueJSON`: pure-Go value wire-format decoding and normalization
- `FuzzCompileAndRun`: arbitrary source strings compiled and executed with tight resource limits
- `FuzzLoadRunner`: arbitrary bytes fed through `LoadRunner`, including valid dumped-runner seeds

Run a short fuzzing pass with:

```bash
CGO_ENABLED=0 go test -run '^$' -fuzz FuzzValueJSON -fuzztime=10s .
CGO_ENABLED=0 go test -run '^$' -fuzz FuzzCompileAndRun -fuzztime=10s .
CGO_ENABLED=0 go test -run '^$' -fuzz FuzzLoadRunner -fuzztime=10s .
```

The native runner fuzzers require a supported host shared library and run with
`CGO_ENABLED=0`. `FuzzValueJSON` remains pure Go.

## Upstream Overrides

The default build uses pinned git dependencies on `https://github.com/pydantic/monty.git`. For local development against a sibling checkout, you can temporarily override them with a Cargo patch:

```toml
[patch."https://github.com/pydantic/monty.git"]
monty = { path = "../monty/crates/monty" }
monty_type_checking = { path = "../monty/crates/monty-type-checking" }
```

See [`RELEASING.md`](../RELEASING.md) for bumping the upstream pin and for the protected-branch release flow: `make release` opens the release-prep PR, then `make publish-release VERSION=vX.Y.Z` tags merged `main`, creates the GitHub release, and warms the Go module proxy.

Run `make shmonty-examples` to build shmonty and execute every
`python-scripts/*.py` example non-interactively. Each script asserts its key
results; the target runs all scripts and reports the failure count at the end.
CI unsets `OTEL_EXPORTER_OTLP_ENDPOINT`, so no collector is required.
Script mode appends a telemetry block (timestamps and durations) and the
trailing value to stdout. Check exit status and script assertions rather
than comparing full stdout byte for byte.
