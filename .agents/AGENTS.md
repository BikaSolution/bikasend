# Bikasend

> **For AI agents**: Read this file first, then **always consult `.agents/rules/` before writing or modifying any code**. The rules are the authoritative source for how this codebase must be written.

Bikasend is a stripped-down Bruno/Postman: a cross-platform desktop app (Go + Wails v2) that opens a
folder (for example a repo of Go services), finds the Swagger/OpenAPI files in it, lists
their operations, and sends an HTTP request for the one you pick. A small download site, exposed
through a Cloudflare tunnel, lists the GitHub releases and hands out the builds.

Bikasend is free and open source (MIT, see `LICENSE`), made by BikaSolutions. The repo layout is
`cmd/`, `internal/<svc>/{api,core}`, `pkg/sdk/<svc>`, `pkg/`, `build/<svc>`, `deploy/<svc>`.

**Out of scope**: scripting, injectable JS, test suites, collections, environments, sync, accounts.
Say no to these unless the human asks for them.

## Security First (CRITICAL)

Security comes before features, speed and convenience. This app sends credentials to internal
APIs, and the site hands out executables to users. A mistake in either is a breach.

- **Think about security on every change.** Before you write code, ask: what input is untrusted
  here, what secret passes through, what can an attacker reach? Write the answer in the PR.
- **Run a security audit often**: before every PR, after every dependency change, after any change
  to `internal/site`, `pkg/httpserve`, `deploy/` or `build/`, and at least once a month. Use the
  `security-auditor` agent (`.agents/agents/security-auditor.md`) and `make security`.
- **Fix or escalate.** A security finding is never "later". Fix it in the same change, or stop and
  tell the human.
- The full rules are in `10-security.md`. They override every other rule.

## Non-negotiables

1. **Security first.** See above and `10-security.md`.
2. **Keep the layout.** Mirror the existing structure and idiom (`00-repo.md`).
3. **Minimal dependencies.** Wails is the only third-party Go module. Everything else is stdlib. No npm, no frontend framework, no CDN. A new dependency needs the human's OK (`00-repo.md`).
4. **Go does the work, the frontend renders.** Scan, parse and HTTP live in `core`. The webview calls Go through the Wails bindings (`20-frontend.md`).
5. **Minimal comments.** If a developer can work it out from the name and code, write no comment (`70-comments.md`).
6. **Tests and test data live in `*_test.go`.** Stdlib `testing` only. No `testdata/` or fixture files (`50-testing.md`).
7. **The website tells the truth.** After a feature, upgrade or release change, audit the download site's text against the code (`90-website-audit.md`).
8. **Never exhaust the machine.** Every parser of untrusted input caps size, nesting depth and total work. Heavy commands run with caps (see Agents below).
9. **Nothing private goes public.** The repo is public: no secrets, tokens, personal emails, internal hostnames, or names of other companies, repos or services.

## Quick Reference

| Action | Command |
| --- | --- |
| Run the app (built, like an install) | `make run` |
| Run the app (dev, live reload) | `make dev` (browser view at <http://localhost:34115>) |
| Test | `make test` |
| Vet / lint / architecture rules | `make vet` · `make lint` · `make arch_lint` |
| Fuzz the spec parsers | `make fuzz` |
| Security scan | `make security` (latest govulncheck + gosec) |
| Build app (this OS) | `make app` → `build/bikasend/bin/` |
| Release | push a tag `vX.Y.Z` — CI builds all platforms (see Releases) |
| Run download site | `make site` (<http://localhost:8091>; set `GITHUB_TOKEN` for a private repo) |
| Quick public URL | `make tunnel` (needs `cloudflared`) |
| Site + named tunnel | `TUNNEL_TOKEN=… GITHUB_TOKEN=… docker compose -f deploy/site/docker-compose.yml up -d --build` |

## Setup

```sh
brew install go golangci-lint
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1
wails doctor                 # checks the platform webview deps
pip install pre-commit && pre-commit install
brew install awscli          # optional: AWS secrets, SSM parameters, SigV4
brew install cloudflared     # only on the machine that hosts the site
```

Platform webviews: macOS uses WebKit (built in). Windows uses WebView2 (built into Windows 11).
Linux needs `libgtk-3-dev` and `libwebkit2gtk-4.1-dev` (build with `-tags webkit2_41`).

## Layout

```tree
cmd/
  bikasend/main.go, wails.json   # Desktop app wiring; version and update URL come from -ldflags
  site/main.go                   # Download site wiring: flags, server, shutdown
internal/
  bikasend/
    api/handlers.go              # App: every exported method is a JS-callable API (thin)
    api/routes.go                # Wails options: window, assets, bindings
    core/service.go              # State: folder, trust, environments, variables, settings
    core/variables.go            # bikasend.json model, {{variable}} resolution, secret guards
    core/http.go                 # Build + send requests: auth, TLS, cookies, timing, redaction, AWS
    core/swagger.go, yaml.go     # Scan + parse Swagger 2.0 / OpenAPI 3 (JSON, YAML), $ref, examples
    core/validate.go             # Check responses against the spec
    core/history.go, curl.go     # History (masked on disk), update check, cURL import/export
    frontend/dist/               # index.html, app.css, app.js, Open Sans; embedded
    port.go, types.go, errors.go # Symlinks to pkg/sdk/bikasend/
  site/
    api/handlers.go, routes.go   # /, /changelog, /feed.xml, /latest.json, /downloads/{tag}/{name}
    api/templates/               # layout.html, index.html, changelog.html (html/template)
    api/static/                  # site.css, icons, Open Sans, mascot
    core/service.go              # Cached GitHub releases (5 min), release notes, asset lookup
    port.go, types.go, errors.go # Symlinks to pkg/sdk/site/
pkg/
  sdk/bikasend/, sdk/site/       # Contracts: port.go (Service), types.go, errors.go
  awscli/                        # Runs the user's aws CLI: secrets, SSM, credentials, SSO login
  sigv4/                         # AWS Signature V4 signing, stdlib only
  github/                        # GitHub Releases client: list releases, signed asset URL
  httpserve/                     # Stdlib router + rate limiter
build/                           # App icon and Wails platform files; site Dockerfile
deploy/site/docker-compose.yml   # site + cloudflared (pinned by digest)
docs/threat-model.md             # Assets, trust boundaries, threats and mitigations
.github/workflows/               # ci.yml (every PR), release.yml (tags), audit.yml (monthly), retry-macos.yml
.golangci.yaml, .go-arch-lint.yml, .pre-commit-config.yaml
```

## Releases

1. Tag and push: `git tag v0.2.0 && git push origin v0.2.0`.
2. `release.yml` creates the GitHub release with generated notes, builds on native macOS, Windows
   and Linux runners with the version and the site URL (repository variable `SITE_URL`) baked in,
   and uploads `Bikasend-<tag>-<platform>.zip` plus `Bikasend-<tag>-modules.txt`. When GitHub
   assigns no macOS runner, `retry-macos.yml` reruns that job, at most 3 times.
3. The site lists the newest 10 releases (drafts hidden) and refreshes every 5 minutes. GitHub
   provides the SHA-256 of every asset; the site shows it. The app asks `/latest.json` for updates.
4. A download goes `/downloads/<tag>/<file>` → GitHub's short-lived signed URL. The site never
   streams files and never exposes its token.

Edit release notes on GitHub; the site renders headings, bullets and paragraphs.

Before you tag, audit the site's static text against the code (`90-website-audit.md`).

## How it works

1. **Open** scans the folder through `os.Root` and parses every `*swagger*` / `*openapi*` JSON or
   YAML file (a YAML file with a JSON twin is skipped). It reads `bikasend.json` but ignores it
   until the user trusts that exact content.
2. **Variables** come from four scopes, narrowest first: session (memory) → environment →
   folder (`bikasend.json`, shared through git) → global (the user's `globals.json`). The spec
   server fills `{{baseUrl}}` last. Secrets are references (`aws`, `ssm`, `env`, `prompt`), never
   values in files.
3. **Send** resolves every `{{variable}}` in Go, applies the guards (allowed hosts, no secrets over
   plain http, no cross-host redirects with secrets), adds OAuth2 or SigV4 auth, sends, then
   redacts secrets from the response and checks it against the spec.
4. The webview only renders. It never holds secret values unless the user clicks reveal.

## Rules — Read Before Writing Code

| Rule | When to apply |
| --- | --- |
| `00-repo.md` | Any change — layout, dependencies, layer boundaries |
| `10-security.md` | **Always** — secrets, untrusted input, the download site, audits |
| `20-frontend.md` | Editing `internal/bikasend/frontend/` or `internal/site/api/templates/` |
| `30-errors.md` | Defining or returning errors |
| `50-testing.md` | Writing or updating tests |
| `60-go-style.md` | All Go code — naming, data passing |
| `70-comments.md` | Writing any comment — keep them rare and short |
| `80-code-review.md` | Reviewing a change — how findings are graded |
| `90-website-audit.md` | After a feature, upgrade or release change — keep the download site's claims true |

## Agentic Coding Structure

`.agents/` is the single source of truth: `rules/`, `skills/` (`create-pr`), `agents/` (`convention-reviewer`,
`security-auditor`).

| Symlink | Points to |
| --- | --- |
| `CLAUDE.md`, `GEMINI.md` | `.agents/AGENTS.md` |
| `.claude/rules`, `.claude/skills`, `.claude/agents` | `../.agents/rules`, `../.agents/skills`, `../.agents/agents` |
| `internal/<svc>/port.go`, `types.go`, `errors.go` | `../../pkg/sdk/<svc>/…` |

Edit the target, never the link. Keep them symlinks: a copy drifts. If a copy or checkout turns
them into plain files, check they match their target, then restore the links. On Windows, clone with
`git config core.symlinks true` (needs Developer Mode).

### Subagents

- Subagents are allowed. Run **at most two at a time**, and only for independent work.
- A subagent gets the same rules. `convention-reviewer` and `security-auditor` spawn none.
- Do not run the same work yourself while a subagent does it.

### Machine safety

The developer's machine runs their editor too. A runaway command can take it down.

- Never run `go test -fuzz` with default parallelism: Go starts one worker per CPU. Use
  `make fuzz` (2 workers, heap target, a 3 GB cgroup on Linux) or copy its flags.
- On Linux, run long or heavy commands (fuzz, `wails build`, scanners, a new parser on big input)
  in their own cgroup, so an overflow kills only that command:
  `systemd-run --user --scope -q -p MemoryMax=4G -p MemorySwapMax=0 <command>`.
- Ask the human before you run anything that can use more than 4 GB or run for more than 10 minutes.
- If a command is killed (exit 137 or 143), stop. Find the cause in the code before you run it again.
