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

Invalid arguments to the new callbacks raise `TypeError`. Enable `/debug`
to inspect callback spans. `open()` and sleep support remain separate work;
use `Path.read_text()` and `Path.write_text()` for file examples.

## Running against a remote worker

`./shmonty --remote host:port` runs the interactive REPL on a Monty worker
that serves `monty.v1.MontySession` over gRPC instead of the native library.
TLS is on by default. Set `MONTY_REMOTE_TOKEN` to send a bearer token (only
over TLS), or `MONTY_REMOTE_INSECURE=1` to dial a local worker without TLS
(no token allowed). `--remote` does not work with a script path, which runs
through the native library.

```bash
MONTY_REMOTE_TOKEN="$LOGFIRE_API_KEY" ./shmonty --remote logfire-us.pydantic.dev:443
```
