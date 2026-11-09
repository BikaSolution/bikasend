SHELL := /bin/bash

APP_DIR := cmd/bikasend
BIN := build/bikasend/bin
# wails is installed with `go install`, so make sure Go's bin folder is on PATH.
export PATH := $(PATH):$(shell go env GOPATH)/bin

.PHONY: test vet lint arch_lint fuzz security dev app run site tunnel

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

# Releases: push a tag (git tag v0.2.0 && git push origin v0.2.0). .github/workflows/release.yml builds
# every platform and publishes the GitHub release; the site picks it up within 5 minutes.

# Private repo: export GITHUB_TOKEN (fine-grained, read-only Contents on the bikasend repo) first.
site:
	go run ./cmd/site

# Quick public URL (*.trycloudflare.com) for the local site. Use deploy/site for a named tunnel.
tunnel:
	cloudflared tunnel --url http://localhost:8091
