.PHONY: release publish-release shmonty repl shmonty-examples proto check-proto remote-realworker

shmonty:
	cd cmd/shmonty && CGO_ENABLED=0 go build -o ../../shmonty .
	@echo "Built ./shmonty — run it with ./shmonty"

repl: shmonty
	./shmonty

shmonty-examples: shmonty
	@failed=0; for script in python-scripts/*.py; do \
		echo "=== $$script ==="; \
		if ! ./shmonty "$$script"; then \
			echo "FAILED: $$script" >&2; \
			failed=$$((failed + 1)); \
		fi; \
	done; \
	if [ "$$failed" -ne 0 ]; then \
		echo "$$failed example(s) failed" >&2; \
		exit 1; \
	fi

release:
	@command -v gh >/dev/null 2>&1 || { echo "gh is required"; exit 1; }
	@gh auth status >/dev/null
	@next_version="$$( \
		if [ -n "$(VERSION)" ]; then \
			echo "$(VERSION)"; \
		else \
			git fetch --tags origin >/dev/null 2>&1; \
			latest_tag="$$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -1)"; \
			if [ -z "$$latest_tag" ]; then \
				echo v0.0.1; \
			else \
				printf '%s\n' "$$latest_tag" | \
					awk -F. '{ sub(/^v/, "", $$1); printf "v%s.%s.%d\n", $$1, $$2, $$3 + 1 }'; \
			fi; \
		fi \
	)"; \
	case "$$next_version" in \
		v*) ;; \
		*) echo "VERSION must start with v, for example v0.0.9"; exit 1 ;; \
	esac; \
	gh workflow run release-prep.yml --ref main -f version="$$next_version"; \
	echo "Triggered release-prep workflow for $$next_version."; \
	echo "After the PR merges, run: make publish-release VERSION=$$next_version"
	@echo "Watch with: gh run list --workflow=release-prep.yml --limit 1"

publish-release:
	@command -v gh >/dev/null 2>&1 || { echo "gh is required"; exit 1; }
	@gh auth status >/dev/null
	@[ -n "$(VERSION)" ] || { echo "VERSION is required, for example make publish-release VERSION=v0.0.14"; exit 1; }
	@case "$(VERSION)" in \
		v*) ;; \
		*) echo "VERSION must start with v, for example v0.0.14"; exit 1 ;; \
	esac
	@gh workflow run release.yml --ref main -f version="$(VERSION)"
	@echo "Triggered release workflow for $(VERSION)."
	@echo "Watch with: gh run list --workflow=release.yml --limit 1"

# Run remote/realworker_test.go against a real local Monty worker (builds
# the pinned upstream monty once; needs git, cargo and uv).
remote-realworker:
	scripts/remote-realworker.sh

# Regenerate the Go code for the vendored monty.v1 proto (remote/proto) into
# remote/internal/montyv1. Needs protoc; the Go plugins are pinned and installed
# into ./.bin by the script.
proto:
	scripts/gen-proto.sh

# Fails if the checked-in generated code is not what the vendored proto
# generates (run in CI).
check-proto: proto
	@if [ -n "$$(git status --porcelain -- remote/internal/montyv1)" ]; then \
		echo "remote/internal/montyv1 is out of date; run 'make proto' and commit the result" >&2; \
		git --no-pager diff --stat -- remote/internal/montyv1 >&2; \
		exit 1; \
	fi
