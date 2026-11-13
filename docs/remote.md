# Remote workers (`remote`)

The `github.com/mdfranz/gomonty/remote` module runs gomonty REPL sessions against an out-of-process `monty.v1.MontySession` worker (or relay) over gRPC, implementing `monty.Backend`.

Host functions, OS / `vfs` handlers, Python limits, snapshots (`Dump` / `LoadRepl`), resets, and OpenTelemetry instrumentation stay in Go on the host side while Python execution runs remotely.

```
Go host (monty.Repl + otelmonty + vfs)
   │
   ▼ remote.NewBackend(transport, ...)
monty.v1.MontySession (gRPC bidi stream, one stream per Repl)
   │   ├── ClientRequest (Configure, Feed, ResumeCall, Reset, Dump, trace_parent)
   ▼   └── WorkerEvent   (Ok, Print, FunctionCall, OsCall, NameLookup, ResolveFutures, Complete, ...)
Remote worker / relay
```

## Connecting and authentication

Dial a worker with `remote.DialGRPC` and wrap it with `remote.NewBackend`:

```go
transport, err := remote.DialGRPC("worker.example.com:443", remote.GRPCConfig{
	Token: os.Getenv("MONTY_REMOTE_TOKEN"),
	Metadata: map[string]string{
		"x-tenant-id": "acme",
	},
})
if err != nil {
	return err
}
defer transport.Close()

backend := remote.NewBackend(transport)
repl, err := monty.NewRepl(monty.ReplOptions{
	ScriptName: "session.py",
	Backend:    backend,
})
```

Transport details:

- **TLS is enabled by default** (minimum TLS 1.2).
- **Bearer auth & metadata:** `GRPCConfig.Token` is sent on stream open as `authorization: Bearer <token>`. `GRPCConfig.Metadata` keys are lower-cased and attached as gRPC metadata headers.
- **Plaintext (`Insecure: true`):** intended for local development and tests. `DialGRPC` refuses `Insecure: true` if `Token` is non-empty or if `Metadata` contains an `authorization` header, so credentials cannot accidentally travel in cleartext.
- **Caller-owned connections:** use `remote.NewGRPCTransport(conn, token)` when your application manages `*grpc.ClientConn` dialing, interceptors, or custom TLS roots directly. `Transport.Close` leaves caller-owned connections open.

## Host integration and supported features

Because `remote.Backend` plugs into `monty.ReplOptions.Backend`, standard gomonty `FeedOptions` work the same way as with the in-process FFI backend:

- **Host functions (`Functions`):** Python calls pause on `FunctionCall`, run the Go `monty.ExternalFunction` on the host, and resume via `ResumeCall`.
- **Async host calls & `asyncio.sleep`:** async host functions return futures (`Waiter`) that resolve when awaited in `ResolveFutures`, and `asyncio.sleep` waits on the host.
- **OS calls (`OSHandler` / `vfs`):** worker OS syscalls pause on `OsCall` and dispatch through your `monty.OSHandler`, so `vfs` filesystems work unchanged. OS operations without a gomonty equivalent (such as opening files for append or exclusive creation) are answered as "not handled", producing an ordinary Python error.
- **Inputs, name lookups, and modules:** `Inputs`, `NameLookup`, and `Modules` cross the wire via `remote`'s MessagePack/Arena value codec.
- **Live print streaming:** `Print` events delivered during a long turn are forwarded immediately to `FeedOptions.Print` (configured via `WithPrintFlushInterval`).

### Current limitations

- **Compiled runners (`monty.New` / `monty.LoadRunner`):** not supported yet (`remote.ErrUnsupported`). Use `monty.NewRepl`.
- **Snapshots while paused:** `Progress.Dump()` returns `remote.ErrUnsupported`; `Repl.Dump()` captures idle REPL state cleanly.
- **Host object methods and attribute access:** return `remote.ErrUnsupported` and stop execution.

## Backend options, snapshots, and reset

Pass `remote.Option` values to `remote.NewBackend`:

| Option | Purpose |
| --- | --- |
| `WithOpenTimeout(d)` | Bounds `NewRepl` stream open + initial `Configure` (default `30s`; `<= 0` disables). |
| `WithTypeCheck(opts)` | Enables worker-side type checking (`Stubs`, `Format`, `Color`). Individual feeds can skip checking with `FeedOptions.SkipTypeCheck`. |
| `WithProfile(name)` | Selects a named server-side profile on a relay (`SessionRefusedError` if unknown; ignored by plain workers). |
| `WithPersistence(p)` | Hints to a relay whether idle/disconnected sessions may be parked (`PersistenceDefault`, `PersistenceEphemeral`, `PersistenceStored`). |
| `WithPrintFlushInterval(d)` | Controls how long the worker buffers print output before streaming `Print` events (`0` flushes each completed line). |

An idle REPL can be dumped and restored or reset in place:

```go
state, err := repl.Dump()
if err != nil {
	return err
}

// Restores on any compatible Backend (local or remote).
restored, err := monty.LoadRepl(state, monty.ReplOptions{Backend: backend})

// Clears remote state and re-applies ReplOptions without closing the stream.
err = restored.Reset(ctx)
```

## Limits and cancellation

- **Resource limits:** `monty.ReplOptions.Limits` (allocations, memory, stack depth, recursion, duration) are sent in `Configure` and enforced remotely by the worker.
- **Context cancellation:** if `ctx` passed to `FeedRun`, `FeedStart`, or `Reset` expires while a request is in flight, the call returns `ctx.Err()` and closes the stream (because a gRPC turn cannot be interrupted mid-flight while keeping the remote interpreter state intact). If `ctx` is already cancelled before sending, the session stays usable.

## Error handling

Errors returned from remote calls use standard `errors.As` / `errors.Is` inspection:

| Error | Meaning |
| --- | --- |
| `*remote.PythonError` | Exception raised inside Python (`Type`, `Message`, `Frames`, `TracebackString()`, optional `Unicode` or `JSON` structured data). Leaves the session usable. |
| `*remote.TypingError` | Worker type checker rejected the snippet (`Diagnostics`). Snippet did not execute; session remains usable. |
| `*remote.SessionRefusedError` | Worker or relay refused session configuration (`Reason`). |
| `*remote.ConnectionLostError` | Stream send/receive failed (`Op`, wrapped cause). Session is closed and cannot be reused. |
| `remote.ErrUnsupported` | Operation not supported by the remote backend yet. |

## Distributed tracing and telemetry

`remote` integrates automatically with OpenTelemetry (`otelmonty`) and Go `context.Context`:

- **`traceparent` propagation:** whenever a context passed to `FeedRun`, `FeedStart`, or `Reset` holds an active `trace.SpanContext`, `remote` attaches a W3C `traceparent` header (`00-<trace-id>-<span-id>-<flags>`) to outbound `ClientRequest` messages (`Configure` on reset, `Feed`, `ResumeCall`, `Reset`, etc.). Workers and relays that read `ClientRequest.trace_parent` join the caller's distributed trace as children of `monty.feed` (or the caller's span if `otelmonty` is not attached).
- **Worker timing attributes:** when a worker reports execution timing and session ID on turn-ending events, `remote` feeds `monty.WorkerStats` to `monty.WorkerStatsSinkFromContext`, so `otelmonty.Handler` records `monty.worker.feed_duration_ms`, `monty.worker.session_duration_ms`, and `monty.worker.session_id` on the `monty.feed` span.

Note that `monty.NewRepl` does not accept a `context.Context`, so the initial `Configure` call carries no `traceparent`.

## CLI (`shmonty`) and examples

- **Interactive CLI:** see [cmd/shmonty/README.md](../cmd/shmonty/README.md) for `--remote <addr>`, `--remote-insecure`, `MONTY_REMOTE_TOKEN`, and `MONTY_REMOTE_INSECURE`.
- **Example program:** see [examples/cmd/remote/main.go](../examples/cmd/remote/main.go), which connects to `MONTY_REMOTE_ENDPOINT` (or shows wiring with `DialGRPC`, `WithTypeCheck`, `WithOpenTimeout`, `otelmonty`, and `PythonError` handling).

## Protocol & testing

- Unit tests in `remote/` use an in-process gRPC test server (`remote/internal/fakeworker`) and run without an external worker (`CGO_ENABLED=0 go test ./...`).
- Live integration test `examples/cmd/remote` (`TestRunAgainstRealWorker`) runs automatically when `MONTY_REMOTE_ENDPOINT` is set.
- The vendored `monty.proto` in `remote/proto/` must match the upstream commit pinned in `Cargo.toml`; `make check-proto` verifies sync and `make proto` regenerates Go protobuf/gRPC bindings.
