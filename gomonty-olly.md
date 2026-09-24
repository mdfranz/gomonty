# Gomonty telemetry plan

## Upstream capability

At gomonty’s pinned Monty revision, `monty-pool` has an optional telemetry feature. It records session and execution spans, logs, metrics, and trace context, and can send records to a host `TelemetryAdapter`. That feature belongs to the async worker pool. Gomonty’s FFI depends directly on the in-process `monty` crate, which has no telemetry feature or adapter, so gomonty cannot enable the pool’s telemetry just by passing through an option.

References in the pinned Cargo checkout:

- `crates/monty-pool/src/telemetry/mod.rs`
- `crates/monty-pool/src/telemetry/tracing.rs`
- `crates/monty-pool/src/telemetry/metrics.rs`
- Gomonty’s `crates/monty-go-ffi/Cargo.toml` lists the direct FFI dependencies.

For Go, Logfire accepts telemetry through standard OpenTelemetry (OTLP); there is no dedicated Logfire Go SDK. This means the Logfire integration should use standard OpenTelemetry rather than introduce any Logfire-specific behavior or dependencies to gomonty.

## Recommendation

Build instrumentation at gomonty’s Go execution boundary (`runner.go` and `dispatch.go`) instead of replacing the in-process interpreter with `monty-pool` or trying to reuse its pool-only recorder. The Go boundary already has access to all pertinent execution data: runtime options, input/output values, host callback invocations and results, captured Python stdout, errors, and execution timings.

### 1. Define an opt-in telemetry contract with context propagation

Define a decoupled, dependency-free telemetry contract in `gomonty`. In Go, trace propagation relies on `context.Context`. The contract must thread `context.Context` so spans created for host callbacks can be passed into `ExternalFunction` and `OSHandler`, enabling any downstream clients (HTTP, database, etc.) invoked inside callbacks to automatically attach as child spans.

Example contract:

```go
type TelemetryHandler interface {
    StartExecution(ctx context.Context, info ExecutionInfo) (context.Context, ExecutionSpan)
    StartCallback(ctx context.Context, info CallbackInfo) (context.Context, CallbackSpan)
    StartWait(ctx context.Context, info WaitInfo) (context.Context, WaitSpan)
    RecordPrint(ctx context.Context, text string)
}

type ExecutionSpan interface {
    End(result Value, err error)
}

type CallbackSpan interface {
    End(result Result, err error)
}

type WaitSpan interface {
    End(err error)
}
```

With no handler configured (`RunOptions.Telemetry == nil`), runtime behavior, performance, and dependencies remain completely unaffected.

### 2. Scope: Phase 1 focused on high-level `Run` and `FeedRun`

Explicitly restrict the initial instrumentation scope to high-level executions:
- `Runner.Run`
- `Repl.FeedRun`

**Rationale**: `Run` and `FeedRun` manage their full dispatch loop synchronously within a single function invocation, guaranteeing deterministic span lifetimes (`defer span.End()`). 

Low-level pause/resume APIs (`Start`, `FeedStart`, and `Progress`) can pause execution across caller turns and may be serialized to disk via `Dump()`. Active in-memory OpenTelemetry spans cannot be serialized across process boundaries. Defer low-level snapshot telemetry to a subsequent phase, which will handle trace continuity via W3C traceparent carrier serialization rather than live span retention.

### 3. Trace hierarchy and timing accounting

Structure the execution trace hierarchy as follows:

```text
monty.run / monty.feed [root execution span]
├── monty.call [child span: external function or OS handler]
│   └── [downstream spans emitted by host callback]
├── monty.wait [child span: time waiting for async future resolution]
└── monty.print [structured log event]
```

Keep the three primary phases of execution time clearly distinguishable:
1. **Python interpreter time**: Time spent inside Rust/FFI (`Start` / `resumeCall`).
2. **Host callback time**: Time spent executing Go callbacks in `dispatchSnapshot`.
3. **Waiter resolution time**: Time spent blocked in `waitForFutureResults`.

On completion of the root span, record aggregate timing attributes, such as `monty.python_duration_ms` (calculated as `total_duration - callback_duration - wait_duration`), giving operators immediate visibility into Python compute time versus host I/O.

### 4. Data sanitization, limits, and stdout multiplexing

Python inputs, kwargs, returned values, and print streams can contain sensitive credentials or multi-megabyte payloads.
- Make payload recording strictly opt-in via telemetry options (e.g. `RecordArguments`, `RecordOutputs`, defaulting to `false`).
- Enforce size truncation limits on strings and collections (e.g., configurable `MaxAttributeBytes`, defaulting to 1024 bytes) similar to upstream’s `serialize_capped` and `length_limit_exceeded` flag.
- When both `RunOptions.Print` and telemetry are active, multiplex output cleanly: stream text to `opts.Print` while also emitting structured `RecordPrint` events to the telemetry handler.

### 5. Support local structured logging (`log/slog`)

Provide a built-in `log/slog` adapter implementing `TelemetryHandler`. This translates execution milestones, callback timings, stdout, and completion outcomes into structured `slog` events without introducing any third-party dependencies:
- Execution start / finish (Info / Error)
- Callback invocation / return (Debug / Info)
- Captured stdout lines (Debug)

### 6. Bridge to OpenTelemetry and preserve core module hygiene

Provide an OpenTelemetry bridge (`otelmonty`) that implements `TelemetryHandler` using `go.opentelemetry.io/otel/trace` and `go.opentelemetry.io/otel/metric`.
- Callers pass their trace-bearing `context.Context` to `Runner.Run`, allowing gomonty spans to seamlessly join the caller's active trace.
- Exporting to Logfire requires zero Logfire-specific code in gomonty; callers simply configure the standard OpenTelemetry OTLP gRPC/HTTP exporter pointing to Logfire's endpoint with their Logfire token.
- **Dependency isolation**: Implement the OTel bridge in a dedicated subpackage or submodule (e.g., `otelmonty/go.mod` or `contrib/otelmonty`) to prevent adding the OpenTelemetry SDK dependencies to gomonty’s lean root `go.mod`.

### 7. Fault isolation and resilience

- Telemetry handlers must be strictly non-intrusive.
- Protect all telemetry callback invocations with `recover()`. Any panic or unexpected failure within a `TelemetryHandler` must be absorbed or routed to an error logger, and must **never** disrupt script execution or alter host callback return values.

### 8. Verification & Acceptance Criteria

- **Zero-overhead / No-op**: Benchmark to verify zero allocations and negligible overhead when telemetry is not configured.
- **Context propagation**: Unit tests verifying that child spans created inside an `ExternalFunction` inherit the trace context from `StartCallback`.
- **Fault tolerance**: Tests ensuring panics inside a telemetry handler do not fail the Python script.
- **Error reporting**: Trace outcomes accurately reflect Python runtime errors, syntax errors, and context cancellations (`context.Canceled`, `context.DeadlineExceeded`).
- **Timing breakdown**: Spans accurately separate Python execution duration from host callback duration and waiter delay.

## Proposed issue split

### Issue 1: Add opt-in structured execution telemetry and `log/slog` adapter
- Define `TelemetryHandler`, `TelemetryOptions`, and span interfaces in `gomonty` with `context.Context` propagation.
- Instrument high-level execution paths (`Runner.Run` and `Repl.FeedRun`) in `runner.go` and `dispatch.go`.
- Add panic recovery, payload redaction/truncation, and timing segregation (Python vs host vs waiter).
- Implement the standard library `log/slog` adapter.
- Unit and benchmark tests for enabled, disabled, and faulty handlers.

### Issue 2: Add OpenTelemetry bridge (`otelmonty`)
- Implement `TelemetryHandler` backed by `go.opentelemetry.io/otel`.
- Package in an isolated submodule (`otelmonty/go.mod`) to keep `gomonty` core dependency-free.
- Support span status mapping, OpenTelemetry semantic attributes, and stdout log events.
- Provide end-to-end examples documenting connection to Logfire via standard OTLP exporters.

### Future: Snapshot trace context continuity
- Support serializing W3C traceparent headers with low-level `Snapshot` states across `Dump` and `Load`.
