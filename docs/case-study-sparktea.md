# Case study: sparktea

sparktea is a Go project that consumes gomonty. Its `codemode` package runs model-written Python in the Monty sandbox with Go-implemented tools. It is the reference consumer that shaped parts of this library, for example the telemetry API and the [`telemetry` example](../examples/cmd/telemetry), whose comments refer to it.

> **Draft:** this page is a stub. The sparktea repository is not part of this one, so only facts already recorded here are stated. Fill in the sections marked TODO.

## What it does

TODO: one paragraph on sparktea's purpose and where code mode fits.

## How it uses gomonty

TODO: describe, from the sparktea source, which of these it uses:

- `ExternalFunction` tools (which ones, and how their signatures are exposed to the model)
- `vfs` or a custom `FileSystem`
- `ResourceLimits` values
- type-checking stubs
- `Telemetry` / `otelmonty`, and where traces are sent

## Lessons for gomonty

TODO: API gaps, surprises and decisions that came out of integrating it. The telemetry design in [`gomonty-olly.md`](../gomonty-olly.md) is one place where consumer needs drove the API.

## Related

- [learning-path.md](./learning-path.md), step 9
- [`examples/cmd/codemode`](../examples/cmd/codemode)
