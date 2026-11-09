SHELL := /bin/bash

APP_DIR := cmd/bikasend
BIN := build/bikasend/bin
# wails is installed with `go install`, so make sure Go's bin folder is on PATH.
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: test vet lint arch_lint fuzz security dev app run release site tunnel

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

arch_lint:
	go run github.com/fe3dback/go-arch-lint@v1.15.0 check

# Hostile spec files: run each parser fuzzer for a while. CI runs 30s each.
# Go starts one fuzz worker per CPU, each with its own heap: keep it to 2, with a heap target.
# On Linux, run inside its own 3 GB cgroup, so an overflow kills the fuzzer and nothing else.
FUZZ_CAP = $(if $(shell systemd-run --user --scope -q true 2>/dev/null && echo ok),systemd-run --user --scope -q -p MemoryMax=3G -p MemorySwapMax=0)
FUZZ = $(FUZZ_CAP) env GOMEMLIMIT=512MiB go test ./internal/bikasend/core/ -run '^$$' -parallel 2 -fuzztime 2m

fuzz:
	$(FUZZ) -fuzz FuzzParseJSON
	$(FUZZ) -fuzz FuzzParseYAML

# govulncheck: known CVEs in deps and stdlib. gosec: insecure code patterns.
# @latest on purpose: an audit with a stale scanner misses new issues.
security:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go run github.com/securego/gosec/v2/cmd/gosec@latest -quiet ./...

dev:
	cd $(APP_DIR) && wails dev -skipbindings $(APP_TAGS)

# Linux builds against WebKitGTK 4.1 (libwebkit2gtk-4.1), as release.yml does.
APP_TAGS := $(if $(filter Linux,$(shell uname)),-tags webkit2_41)

app:
	cd $(APP_DIR) && wails build -clean -skipbindings -trimpath $(APP_TAGS)

# Build, then start the app like an installed one. Use `make dev` for live reload while coding.
run: app
ifeq ($(shell uname),Darwin)
	open $(BIN)/Bikasend.app
else
	./$(BIN)/Bikasend
endif

# Release: make release VERSION=v0.2.0. Checks first, asks before anything leaves this machine.
# The tag starts .github/workflows/release.yml, which builds every platform; the site lists it within 5 minutes.
# Tags are protected on GitHub: once pushed, only an admin can move or delete one.
release:
	@set -euo pipefail; \
	last="$$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)"; \
	next="$$(echo "$$last" | awk -F. '{printf "%s.%s.%d", $$1, $$2, $$3 + 1}')"; \
	v="$(VERSION)"; \
	if [ -z "$$v" ]; then echo "usage: make release VERSION=$$next   (last release: $$last)"; exit 1; fi; \
	[[ "$$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$$ ]] || { echo "VERSION must look like v1.2.3, got $$v"; exit 1; }; \
	[ "$$(git branch --show-current)" = main ] || { echo "release from main only"; exit 1; }; \
	[ -z "$$(git status --porcelain)" ] || { echo "commit or stash your changes first"; exit 1; }; \
	git fetch -q --tags origin main; \
	[ "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" ] || { echo "main differs from origin/main: pull or push first"; exit 1; }; \
	if git rev-parse -q --verify "refs/tags/$$v" >/dev/null || [ -n "$$(git ls-remote --tags origin "refs/tags/$$v")" ]; then echo "$$v already exists"; exit 1; fi; \
	[ "$$(printf '%s\n%s\n' "$$last" "$$v" | sort -V | tail -1)" = "$$v" ] || { echo "$$v is not newer than $$last"; exit 1; }; \
	$(MAKE) --no-print-directory vet test; \
	file="$(APP_DIR)/wails.json"; \
	cur="$$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' "$$file")"; \
	echo; echo "Release $$v (last: $$last) from $$(git rev-parse --short HEAD)"; \
	[ "$$cur" = "$${v#v}" ] || echo "  bump $$file productVersion $$cur -> $${v#v} (new commit on main)"; \
	echo "  Site text checked against the code? See .agents/rules/90-website-audit.md"; \
	read -r -p "Push $$v and start the release build? [y/N] " ok || ok=""; \
	case "$$ok" in y|Y) ;; *) echo "cancelled, nothing changed"; exit 1 ;; esac; \
	if [ "$$cur" != "$${v#v}" ]; then \
	  NEW="$${v#v}" perl -pi -e 's/("productVersion":\s*")[^"]*"/$${1}$$ENV{NEW}"/' "$$file"; \
	  git add "$$file"; TZ=UTC git commit -q -m "chore(release): $$v"; \
	  git push -q origin main; \
	fi; \
	TZ=UTC git tag -a "$$v" -m "Bikasend $$v"; \
	git push -q origin "$$v"; \
	echo "Pushed $$v. Follow the build: gh run watch \$$(gh run list -w release -L 1 --json databaseId --jq '.[0].databaseId')"

# Private repo: export GITHUB_TOKEN (fine-grained, read-only Contents on the bikasend repo) first.
site:
	go run ./cmd/site

# Quick public URL (*.trycloudflare.com) for the local site. Use deploy/site for a named tunnel.
tunnel:
	cloudflared tunnel --url http://localhost:8091
