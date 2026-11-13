# shmonty sandbox

Run `make shmonty` from the repository root, then `./shmonty` for the
interactive REPL or `./shmonty python-scripts/basics.py` for a script.

Python filesystem operations use a memory filesystem seeded with
`/sandbox/readme.txt` and `/sandbox/examples/hello.txt`. Use `pathlib.Path`
to read, write, and explore it, including `Path.rglob()` and `os.walk()`.
The virtual environment contains `HOME=/sandbox` and `SHMONTY_ENV=sandbox`;
it does not inherit the host environment. `import hostmod` exposes the
attributes `name` (`"shmonty"`) and `root` (`"/sandbox"`).

Filesystem changes persist across feeds and `/execute` calls in one REPL
session. Rewind starts a fresh filesystem and replays the retained inputs,
just as it rebuilds Python state. Each standalone script invocation starts
fresh. The `/execute` command reads its script from the host filesystem;
Python paths refer to the memory filesystem.

Available host callbacks accept positional or keyword arguments:

- `host_time()` returns the current time as an RFC3339 string.
- `host_calc(op, a, b)` supports `add`, `sub`, `mul`, and `div`, returning
  a floating-point number. It converts integer inputs to float64, so large
  integers may lose precision. Division by zero raises `ZeroDivisionError`;
  unknown operations raise `ValueError`.
- `host_echo(value)` returns `{"value": value, "source": "shmonty"}`.
- `host_fail(msg)` raises a catchable Python `ValueError` with the message.
- `host_sleep(seconds, value=None)` is an async host function: it returns a
  future that resolves to `value` after `seconds` (0 to 30), so
  `await host_sleep(0.1, "x")` and `asyncio.gather(host_sleep(...), ...)`
  exercise futures, locally and on a remote worker.

Invalid arguments to the callbacks raise `TypeError` (or `ValueError` for an
out-of-range delay). Enable `/debug` to inspect callback spans. `open()` is
separate work; use `Path.read_text()` and `Path.write_text()` for file
examples.

## Session commands

These slash commands act on the session, not on Python, so they are not kept
in the history that rewind replays:

- `/execute <path>` runs a file as one snippet, as if typed in.
- `/reset` empties the session: variables, definitions and the in-memory
  filesystem return to their starting state, and the history is cleared.
- `/dump <path>` writes the REPL's variables and definitions to a file.
- `/load <path>` replaces the session with one restored from `/dump`. The
  in-memory filesystem and the history start fresh.
- `/debug` toggles the telemetry pane.

Tab completes the path of `/execute`, `/dump` and `/load`. A snippet runs in
the background, so the screen stays live: Ctrl+C cancels the running turn (a
second Ctrl+C quits). Cancelling a remote turn ends its session, so shmonty
starts a new one and earlier state is gone. A turn on the native library
cannot be interrupted mid-computation; Ctrl+C takes effect when it next waits
on the host.

A dump is only loadable by the kind of backend that wrote it: a native dump
cannot be loaded on a remote worker, or the other way round. A remote worker
must support session storage to take a dump, and refuses otherwise.

## Running against a remote worker

`./shmonty --remote host:port` runs the interactive REPL on a Monty worker
that serves `monty.v1.MontySession` over gRPC instead of the native library.
TLS is on by default. Set `MONTY_REMOTE_TOKEN` to send a bearer token (only
over TLS), or `MONTY_REMOTE_INSECURE=1` to dial a local worker without TLS
(no token allowed). `./shmonty --remote host:port script.py` runs a script on
the worker too, as one snippet in a session. Errors show the worker's
traceback. The status line shows `remote:<endpoint>` while a worker is in use.

```bash
MONTY_REMOTE_TOKEN="$LOGFIRE_API_KEY" ./shmonty --remote logfire-us.pydantic.dev:443
```
