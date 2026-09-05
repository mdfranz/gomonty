# Upstream Refresh: bump pinned Monty from c9802b5 to main — DONE

## Status: executed on branch `mdfranz/upstream-refresh-monty-33556ba`, not yet pushed

This is now a record of what happened, not a plan to execute. It supersedes
the original version of this file (which stopped after Step 2 of
[`.agents/skills/upstream-refresh/SKILL.md`](.agents/skills/upstream-refresh/SKILL.md)
and estimated scope from a partial commit-history read). Steps 3–7 of the
skill are done; Step 8 (branch/commit) is done — the commit on
`mdfranz/upstream-refresh-monty-33556ba` has the full technical narrative.
Steps 8's push/PR, 9 (merge + release trigger), and 10 (tag/release) are
intentionally **not done** — stopped here at the user's request.

## Freshness check (re-run before resuming)

Re-verified 2026-09-05, after the code changes below were already committed:
`gh api repos/pydantic/monty/commits/main --jq '{sha, date: .commit.committer.date}'`
still returns `33556ba6e98e788aa1e94b3f342bddfede7d90ef` /
`2026-09-05T11:31:04Z` — the exact commit this refresh targeted. Upstream had
not moved further as of that check. Re-run the same command before resuming
this work in a later session; if it returns a different SHA, the diff on
`mdfranz/upstream-refresh-monty-33556ba` is against a now-stale target and
Steps 1–2 of the skill should be repeated for whatever landed since.

## What the original plan got right

- Pin, target SHA, and 347-commit count were all correct.
- `object.rs`/`run_progress.rs` were confirmed FFI-affecting (both PRs listed
  do touch them — `gh api .../pulls/<n>/files` needs `?per_page=100` or it
  silently truncates at 30 files and hides exactly this, which is what
  happened on the first pass here).
- `#682` (`Dataclass` → `ClassInstance`) and `#806` (`max_suspensions`) were
  correctly flagged as the highest-confidence, highest-impact items.
- The itertools/iterator commits and the collections (deque/namedtuple/
  defaultdict/Counter) and `functools.partial` additions turned out to be
  genuinely internal-only, as the plan suspected: none of them introduce a
  `MontyObject` variant (confirmed by reading the enum directly, 27 variants
  total, none of these among them), so nothing crosses the FFI boundary for
  them — no gomonty changes were needed there.

## What the original plan missed

It scoped this as "the wire format + Go type additions" per the skill's
usual shape. The actual pin bump also required:

- **Crate restructuring**: `monty`'s public re-exports shrank drastically —
  `MontyObject`, `ExcType`, `MontyException`, `ResourceLimits`,
  `ResourceTracker`, `PrintWriter`, `ExtFunctionResult`, `NameLookupResult`,
  etc. all moved to a new `monty-types` crate, which gomonty now depends on
  directly. The `monty_type_checking` crate's package name also changed
  (`monty_type_checking` → `monty-type-checking`, hyphenated; its `[lib]`
  name is unchanged).
- **Resource-tracking redesign**: the `NoLimitTracker`/`LimitedTracker`
  generic split on `MontyRepl<T>`/`RunProgress<T>`/`ReplProgress<T>` is gone.
  A single non-generic `ResourceTracker` now always carries `ResourceLimits`,
  with `max_recursion_depth`/`max_suspensions` defaulting to 1000 rather than
  being truly unbounded — this is also *how* `max_suspensions` landed, not an
  addition alongside unchanged tracker plumbing. Collapsed gomonty's
  `StoredRepl`/`StoredProgress` from 2/4-way enums to non-generic ones;
  `crates/monty-go-ffi/src/lib.rs` shrank by 627 net lines.
- **`max_allocations` was removed** from upstream `ResourceLimits` entirely
  (not flagged by the plan at all) — dropped from the Go/Rust wire structs
  and the public `ResourceLimits` Go struct.
- **`MontyRun::new`/`MontyRepl::new` gained a required `CompileOptions`
  param** (unrelated to resource limits — controls `assert` message
  introspection). gomonty passes `CompileOptions::default()`; not yet
  exposed to Go callers.
- **Type-check API reworked**: `monty_type_checking::type_check` (a free
  function) is gone; type checking is now a `TypeChecker::run(&mut self, ...)`
  method that takes a `TypeCheckingConfig` (format + color) up front and
  returns `TypeCheckingDiagnostics<'a>` borrowing the `TypeChecker`. gomonty's
  `monty_go_error_display(error, format, color)` API lets a caller pick
  format/color *after* the error is returned, which the new borrowed,
  config-at-check-time diagnostics type can't support directly — worked
  around by storing the type-check inputs (source/script_name/stubs) and
  re-running the (already-known-to-fail) check on each display call instead
  of storing the diagnostics themselves.
- **`OsCall.function` → `function_call: OsFunctionCall`**: no longer a bare
  `Display`-only value with separate `args`/`kwargs` fields; now a tagged
  enum with `.name()` and `.to_args()`. `FunctionCall.method_call: bool` is
  gone too, replaced by `object_id: Option<MontyUuid>` (routes a call to a
  host-object receiver by identity; the receiver is no longer in `args`).
- **Two more new `MontyObject` variants beyond what the plan named**:
  `FileHandle` (the result of `open()`) alongside the plan's `NotImplemented`
  and `datetime.time`.
- **`StackFrame.preview_line` changed from `Option<String>` to
  `Option<Arc<str>>`**, a small but real breaking change to `wire.rs`'s
  `WireFrame::from`.

## Known gap, deliberately not closed

`dispatch.go`'s `IsMethodCall` branch used to look up `Args[0].Dataclass().Methods[name]`
to dispatch a method call to a host-attached handler. This was **already
non-functional before this refresh**: `Dataclass.Methods` carried
`json:"-"`/no wire tag, so it never survived the Rust round-trip — `Args[0]`
arriving back from the FFI was always a fresh `Dataclass` with an empty
`Methods` map. Upstream's new `object_id`-based routing doesn't have an
equivalent self-in-args shape to lean on either. Simplified
`dispatchSnapshot` to route a method call through the same by-name
`cfg.functions` lookup as a plain external function (correct for the common
case; wrong only for genuinely overloaded per-instance method names, which
had no working implementation to preserve anyway). A real host-object
registry keyed by `ClassInstance`/`ClassType` uuid identity would need to be
designed and built from scratch — out of scope here.

## Verification performed

- `cargo test -p monty-go-ffi` — 15/15 (new tests: `ClassInstance`,
  `NotImplemented`, aware/naive `Time` round-trips; `FileHandle` and
  `Dataclass`-wire-kind explicitly-rejected-as-input cases).
- `cargo fmt -p monty-go-ffi -- --check` clean.
- `CGO_ENABLED=0 go build/vet/test ./...` — against a **freshly rebuilt**
  `internal/ffi/lib/darwin_arm64/libmonty_go_ffi.dylib`
  (`MONTY_GO_FFI_SKIP_HEADER=1 scripts/build-go-ffi.sh aarch64-apple-darwin`),
  not the stale prebuilt one the repo ships — the Go test suite loads that
  `.dylib` via `purego`/`go:embed` with no cgo, so running it against the old
  binary would have silently validated nothing about the Rust changes.
- Ad hoc end-to-end smoke test (throwaway `go run`, not committed) exercising
  the real interpreter: `NotImplemented`, naive and timezone-aware
  `datetime.time`, and `ClassInstance` round-tripped both directions
  (host-constructed instance passed in as an input and read back inside
  Python; a sandbox-defined class instance returned as output) — all correct.
  Confirmed `ClassType.HostDefined` must be `true` for a host-constructed
  instance to be usable as an input (upstream rejects a
  `HostDefined: false`/sandbox-origin instance sent back in), which is now
  called out in the `ClassType` doc comment.

## Not done in this pass

- The other five release platforms' shared libraries (linux amd64/arm64
  glibc+musl, windows amd64) were not rebuilt locally — per `RELEASING.md`
  those come from the `release-prep` GitHub Actions workflow
  (`make release`), not from local dev. `internal/ffi/checksums.txt` is
  correspondingly untouched; it's regenerated by that same workflow.
- `CompileOptions`'s new `assert_message_annotations` knob is not exposed
  through gomonty's public API — `CompileOptions::default()` is used
  unconditionally. Exposing it would be a small, separate follow-up.
- Push, PR, merge, and release (skill Steps 8's push/PR through 10) —
  stopped here at the user's request; the branch is committed locally only.

## Next steps for whoever picks this up

1. Re-run the freshness check above.
2. Review the local commit on `mdfranz/upstream-refresh-monty-33556ba`
   (`git log -1 --stat` / `git show`).
3. Push and open the PR (`git push -u origin mdfranz/upstream-refresh-monty-33556ba`,
   `gh pr create`), or fold in further changes first if upstream has moved.
4. After merge: `make release` per `RELEASING.md` — that workflow rebuilds
   all six platform libraries, regenerates `checksums.txt`, and validates the
   assembled tree before tagging.
5. Ping sparktea's `MONTY-PLAN.md` — its "Monty ↔ gomonty version drift"
   section should note the pin bump, and its `flattenValue` helper (compound
   `Value` → plain JSON for a `run_code` script's return value) should get
   cases for `NotImplemented`, `Time`, `ClassInstance`, and `FileHandle`.
