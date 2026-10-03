---
name: release
description: Cut a gomonty release. Use when the user wants to release, tag, publish, or ship a new gomonty version, run release-prep, or asks what the release process is. Covers make release, the release-prep PR, make publish-release, and recovering from failures.
---

# Releasing gomonty

Full reference: `RELEASING.md` and `docs/ci.md`. This skill is the operating checklist.

Releases are outward-facing and cannot be cleanly undone (a pushed tag and a warmed Go proxy are permanent). **Get the user's explicit go-ahead for the version before dispatching either workflow.**

## Before you start

- Work from a clean, up-to-date `main`; `release-prep` must run on `main`.
- `verify` is green on the commits being released.
- Any upstream Monty pin bump is already merged. A pin or lockfile change after release-prep invalidates `checksums.txt`.
- Decide the version. `make release` bumps the patch of the latest `vX.Y.Z` tag; use `VERSION=vX.Y.Z` to override.

## Steps

1. **Prepare.** `make release` (or `make release VERSION=vX.Y.Z`). This dispatches `release-prep`, which builds the header and all six libraries, updates `Cargo.toml`, `Cargo.lock` and `internal/ffi/checksums.txt`, validates the tree, and opens a `release-prep/<version>` PR. Watch it with `gh run list --workflow=release-prep.yml --limit 1`.
2. **Review and merge** the release-prep PR. It is the only PR that should contain `internal/ffi/lib/**`, the header or `checksums.txt`.
3. **Publish.** After the merge, `make publish-release VERSION=vX.Y.Z`. This dispatches `release`, which verifies the committed tree against `checksums.txt`, runs the tests, tags the dispatched commit, creates the GitHub release with notes from `git log`, and warms the Go proxy. It rebuilds nothing, because the FFI builds are not reproducible.
4. **Confirm.** `gh release view vX.Y.Z`, and `go list -m github.com/mdfranz/gomonty@vX.Y.Z`.

## When it fails

| Symptom | Fix |
| --- | --- |
| `release` says Cargo.toml version is wrong | release-prep was not run or its PR is not merged; do that, then retry. |
| `release` says the tree does not match `checksums.txt` | Something changed libraries or the lockfile after release-prep. Re-run release-prep from current `main`. |
| `release-prep` says the tag already exists | That version was already published; choose the next one. |
| `release-prep` reports nothing to commit | The tree is already current; there is nothing to release. |

## Do not

- Commit locally built libraries, the header or `checksums.txt` by hand.
- Re-run `release` expecting rebuilt binaries.
- Delete or move a pushed tag without asking; Go proxies cache it.

## Known caveat

`otelmonty` is a nested module but the release only tags `vX.Y.Z`, so it has no `otelmonty/vX.Y.Z` tag yet (issue #43). Mention this if the user expects `otelmonty` to be versioned.
