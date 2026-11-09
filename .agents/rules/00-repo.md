# Repo Overview

- Go 1.27. Two services: `bikasend` (Wails desktop app) and `site` (download site).
- Keep the existing layout. When unsure how to lay something out, find the closest existing code
  and mirror it.
- Prefer the smallest change that satisfies the request.

## Study the Code Before Changing It

Read the file you will change, its test, and the same layer in the other service. Mirror what is
there. Do not add a new pattern when one already exists.

## Keep It Flat (KISS)

- Add code to the existing file. Do not create a new file, folder, or package for a new feature.
- Split only when a file is unmanageable — not "for later", not for neatness.
- Create only the layer folders a service needs right now (`site` has `api/` only).

## Layer Boundaries (per service)

```sh
cmd/<svc>/main.go          # Wiring only — flags, deps, start. No logic.
internal/<svc>/api/        # Entry layer: Wails bindings (bikasend) or HTTP handlers (site)
internal/<svc>/core/       # Logic — pure, testable, no Wails or window code
internal/<svc>/port.go     # Symlink to pkg/sdk/<svc>/port.go (also types.go, errors.go)
pkg/sdk/<svc>/             # Contract: import this, never the symlinked internal/<svc>
pkg/                       # Shared packages
```

- `api` stays thin: it calls the `bikasend.Service` port and returns the result.
- `core` never imports Wails. Only `internal/bikasend/api` and `cmd/bikasend` do.
- A service never imports another service's `internal/` packages.
- Import `pkg/sdk/<svc>`, never `internal/<svc>` (Go compiles the symlinks as a second package).

## Dependencies (CRITICAL)

- Wails (`github.com/wailsapp/wails/v2`) is the only direct third-party Go module.
- Everything else comes from the stdlib: `encoding/json`, `net/http`, `io/fs`, `html/template`,
  `log/slog`, `flag`.
- No npm, no `package.json`, no bundler, no frontend framework, no CDN links.
- Need a dependency anyway? Stop and ask the human. A new direct dependency without that OK is a
  **Blocker** in review. Every dependency is attack surface (`10-security.md`).
- Code copied in from elsewhere: copy only what is used, check its licence allows it, and strip
  its third-party imports (a logging library → `log/slog`, a config library → `flag`, no otel or
  prometheus). `pkg/httpserve` is the example.

## Swagger Parsing

- Swagger 2.0 and OpenAPI 3.0/3.1, JSON and YAML. The YAML parser (`core/yaml.go`) is a stdlib
  subset: block and flow collections, scalars, block scalars. Anchors, tags and multi-document
  files are refused. A YAML file with a JSON twin is skipped.
- Specs are parsed as generic JSON trees; `$ref` works for any node and for other files in the
  folder (`docs.deref`).

## External tools

- AWS access goes through the user's own `aws` CLI (`pkg/awscli`), never the AWS SDK. That keeps
  SSO, profiles and MFA working and adds no Go modules. SigV4 signing is `pkg/sigv4` (stdlib).

## Cross-platform

- Build paths with `filepath`, never with `/` string joins.
- No OS-specific code outside what Wails wraps. If you must, use a build-tagged file.
- Release builds run in CI on native macOS, Windows and Linux runners (`release.yml`). Locally,
  `make app` builds for this OS.
