# Learning path: building Go tools around Monty

A sequence of exercises for learning Monty by building with it from Go. Each step builds on the last and points at runnable code. Run the examples with `cd examples && CGO_ENABLED=0 go run ./cmd/<name>` after building the native library for your host (see [contributing.md](./contributing.md)).

Read [architecture.md](./architecture.md) first if you want the mental model, or just start and come back.

## 1. Hello, sandbox
Run `40 + 2` and read the `Value` back. Then trigger a `SyntaxError` and a `RuntimeError` and look at the error types in `errors.go`.
- Code: [`examples/cmd/example`](../examples/cmd/example)

## 2. Give the sandbox a tool
Register an `ExternalFunction` and call it from Python. Return a value, then `monty.Raise` an exception and catch it with `try/except` in the guest code.
- Code: [`examples/cmd/telemetry`](../examples/cmd/telemetry), the Quick Start in [`go/README.md`](../go/README.md)

## 3. Give the sandbox a filesystem
Serve `pathlib` calls from `vfs.NewMemoryFS`, then implement `vfs.FileSystem` yourself over a real directory with path checks.
- Code: [`vfs/example_test.go`](../vfs/example_test.go)

## 4. Bound the untrusted code
Set `ResourceLimits` (`MaxDuration`, `MaxMemory`, `MaxSuspensions`) and write guest code that exceeds each one. Note which errors you get and that `context` cancellation also stops a run.

## 5. Type-check before running
Compile with `CompileOptions{TypeCheck: true}` or call `Runner.TypeCheck`, supplying stubs that describe your tools. Use the type errors as feedback to send back to a model.

## 6. Drive execution yourself
Use `Runner.Start` and handle `Snapshot`, `NameLookupSnapshot` and `FutureSnapshot` by hand. `Dump` a paused snapshot, reload it with `LoadSnapshot` and resume it. Return `Pending` from a tool and resolve it later with `ResumeResults`.

## 7. Persistent sessions
Use `NewRepl` and `FeedRun` to keep state across snippets, as an agent would across turns. Try the same interactively with `shmonty` (`make repl`).
- Code: [`cmd/shmonty`](../cmd/shmonty), sample scripts in `python-scripts/`

## 8. Observe it
Pass `monty.SlogHandler` and read the logs. Then export spans with `otelmonty` to an OTLP backend, with argument recording on and off.
- Code: [`otelmonty/cmd/example`](../otelmonty/cmd/example), [`gomonty-olly.md`](../gomonty-olly.md)

## 9. Build a code-mode tool loop
Put it together: a Go program that exposes several tools, lets a model write one Python program that calls them, runs it under limits and a virtual filesystem, and returns the result and `print()` output to the model. The [`examples/cmd/codemode`](../examples/cmd/codemode) example is the offline skeleton (the model's code is hard-coded). Replace that string with a call to an LLM client, send type errors and exceptions back as the next prompt, and you have the loop.

The pattern comes from "code mode" agents such as those in Pydantic AI's ecosystem; see [monty-context.md](./monty-context.md). [case-study-sparktea.md](./case-study-sparktea.md) describes one Go consumer.

## Going deeper
- Read the Rust side: `crates/monty-go-ffi/src/lib.rs`, then the upstream `monty` crate it wraps.
- Add a benchmark or fuzz target ([contributing.md](./contributing.md)).
- Follow an upstream bump end to end with the `upstream-refresh` skill.
