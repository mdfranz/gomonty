# Case study: sparktea

[sparktea](https://github.com/mdfranz/sparktea) is a terminal chat UI (bubbletea) built on [pydantic-ai-go](https://github.com/Kludex/pydantic-ai-go), a Go port of PydanticAI. It talks to several model providers and, with `/code` enabled, gives the model a `run_code` tool that executes Python in Monty through gomonty. It is the first real consumer of this library and shaped parts of it, notably the telemetry API (see the [`telemetry` example](../examples/cmd/telemetry)).

This page describes how sparktea uses gomonty, based on its public source (`codemode/`, `MONTY-PLAN.md`, `README.md`). It is a snapshot; check the sparktea repo for current behavior.

## What it does

PydanticAI's "code mode" replaces one-tool-call-per-turn with a single `run_code` tool: the model writes a Python script, the host runs it in a sandbox, and the result comes back in one round trip. pydantic-ai-go had no equivalent, so sparktea implements it as an `ai.Capability` in its own `codemode/` package. Monty is the sandbox: no Docker or subprocess, and a few milliseconds to start.

`/code` is off by default. In scripted mode, `sparktea -code -prompt "..."` runs one turn without the TUI, and `monty_codegen.sh` runs 23 small tasks to measure how well a model's generated Python survives Monty's language limits.

## How it uses gomonty

| gomonty feature | How sparktea uses it |
| --- | --- |
| `monty.New` + `Runner.Run` | One runner per `run_code` call, script name `run_code.py`. |
| `ResourceLimits` | 5 s wall clock, 64 MiB memory, recursion depth 100, `MaxSuspensions` 10 as a backstop. |
| `WriterPrintCallback` | Captures `print()` output into a `strings.Builder` returned with the result. |
| Error types | Syntax errors, runtime errors, limit violations and type errors are all returned to the model as retry prompts. |
| `Telemetry` / `TelemetryOptions` | Wraps `otelmonty.Handler` so `monty.run` spans nest under the model-request spans exported to Logfire. |
| Functions, `vfs`, `TypeCheck` | Not used yet. `RunOptions.Functions` is never set, so scripts cannot call sparktea's own tools. |

The tool description sent to the model spells out Monty's limits (the available stdlib modules, no class inheritance, no generators, recursion cap) so the model writes code Monty can run. A test matrix in `codemode/codemode_test.go` pins which language and stdlib features work on the embedded Monty release.

## Design decisions worth copying

- **Failures go back to the model as retries.** A script error is returned as `ai.Retryf(...)`, not a hard failure, so the model can fix its code. A cumulative cap of 20 retries bounds a model stuck in a loop.
- **Limits are set for the UI, not just safety.** The 5 s cap exists so a runaway script cannot stall the terminal.
- **Telemetry content is opt-out.** sparktea wraps the OTel handler so one setting also suppresses `print()` events, which `otelmonty` records independently of `RecordArguments`/`RecordOutputs`.
- **Show the runtime version.** The header displays the linked gomonty version and its embedded Monty release (`codemode/version.go`), which must be updated when the gomonty pin moves to a different upstream Monty.

## Lessons for gomonty

Read from sparktea's workarounds; each is a candidate API improvement here.

- **`Value` to JSON needs a helper.** `Value.MarshalJSON` wraps every scalar in a `{"kind", "value"}` envelope, and `Raw()` on a list returns still-wrapped `[]monty.Value`. sparktea wrote `flattenValue` (`codemode/flatten.go`) to recurse into plain Go types before returning results to the model.
- **Kinds are compared as strings.** The `valueKind*` constants are unexported, so sparktea switches on `v.Kind()` against literals like `"list"` and `"none"`. Exporting the constants would remove that fragility.
- **The print hook is separate from the content options.** A consumer that wants "no content in telemetry" has to wrap the handler, as sparktea does with `RecordPrint`.
- **Dependency management was awkward early on.** `MONTY-PLAN.md` records pinning a personal fork by commit through a `replace` directive, with native libraries rebuilt for only one of six platforms. sparktea now requires `github.com/mdfranz/gomonty` and `.../otelmonty` directly, which is the intended path (see [`RELEASING.md`](../RELEASING.md)).

## Next step for sparktea

The documented follow-up is "full code mode": registering sparktea's own tools (such as web search) as `ExternalFunction`s so a script can compose them. gomonty already supports this; [`examples/cmd/codemode`](../examples/cmd/codemode) shows the pattern with a Go tool and a virtual filesystem.

## Related

- [learning-path.md](./learning-path.md), step 9
- [architecture.md](./architecture.md)
