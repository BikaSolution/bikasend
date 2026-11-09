# Website Content Audit

The download site (`internal/site/api/templates/index.html`) and `README.md` make promises about the app:
features, formats, requirements, install steps. Most of that text is static. Releases come from
GitHub, but the rest goes stale silently when the code changes. A wrong requirement or a missing
feature costs users time and trust.

## When to audit

Audit the site in the same change, before the PR, when you:

- add, change or remove a feature of the app (scan, parse, send, UI),
- upgrade Go, Wails, or a platform dependency (the minimum OS versions follow these),
- change `.github/workflows/release.yml` (platforms, architectures, asset names),
- change security behaviour the site describes (secrets, signing, checksums),
- prepare a release tag.

A change in one of these areas that leaves the site wrong is a **Blocker** in review.

## How to audit

1. List what changed: `git diff <last-tag>...HEAD --stat` (or the PR diff).
2. Go through every row in the table below that the change touches. Read the source of truth.
   Do not check from memory.
3. Fix the template in the same change, and the same claim in `README.md` (features, download
   table, badges). Keep the voice: short, plain, no hype.
4. Check the page renders: `go test ./internal/site/...`, then look at it in light and dark mode
   and at 375px wide (`20-frontend.md`).
5. Write `Website audit: <no change needed | updated: …>` in the PR description.

## Claims and their source of truth

| Claim on the site | Section | Source of truth |
| --- | --- | --- |
| Finds Swagger/OpenAPI JSON and YAML, skips `.git`, `node_modules`, `vendor`, never reads outside the folder | Features, FAQ | `scanSpecs` in `internal/bikasend/core/swagger.go`; `os.OpenRoot` in `core/service.go` |
| Lists operations by tag; reloads when a spec changes | Features | `app.js` `renderOps`; `Changed` in `core/service.go`; `watch` in `api/handlers.go` |
| Swagger 2.0, OpenAPI 3.0 and 3.1; `$ref` across files | Features | `parse`, `deref`, `schemaType` in `core/swagger.go`; `core/yaml.go` |
| `{{variables}}`, environments, a shared `bikasend.json` | Features, How it works | `core/variables.go`, `core/service.go` |
| AWS Secrets Manager and Parameter Store through the user's `aws` CLI | Features, FAQ | `pkg/awscli`; `LoadSecrets` in `core/http.go` |
| Secrets only to listed hosts, not over plain http, not along cross-host redirects | Features | `guard` and `CheckRedirect` in `core/http.go` |
| Refuses secret-looking values in `bikasend.json` | FAQ | `guardFileValue` in `core/variables.go` |
| JSON tree, search, timing, images, spec check, cURL without secrets | Features | `app.js` `renderBody`; `core/validate.go`; `core/curl.go` |
| The hero screenshot shows the current app | Hero | `static/app.webp`: retake it (generic example repo) when the UI changes |
| Bodies pre-filled from examples, enums, defaults | Features | `example` in `core/swagger.go` |
| Path, query, header params and auth headers set up | Features | `buildOperation` in `core/swagger.go` |
| Secrets stay in memory, never on disk or in logs | Features | `remembered` and `store` in `frontend/dist/index.html`; `10-security.md` |
| No CORS: requests run natively | Features | `Send` in `internal/bikasend/core/service.go` |
| About 10 MB, offline, no account, no telemetry | Features | `make app` output size; network calls in `frontend/dist/index.html` and `core/` |
| Remembers the folder; base URL per spec | How it works | `load`/`store` keys in `frontend/dist/index.html` |
| Filter by path, method or summary | How it works | `renderOps` in `frontend/dist/index.html` |
| `⌘ Enter` / `Ctrl Enter` sends; status, time, size, headers, pretty JSON | How it works | `keydown` and `send` in `frontend/dist/index.html`; `Send` in `core/service.go` |
| Platforms offered and architectures ("Universal", "64-bit") | Download | `matrix` in `.github/workflows/release.yml` |
| Asset names map to macOS / Windows / Linux | Download | `out=` in `release.yml`; `platform` in `internal/site/core/service.go` |
| macOS minimum version | Download | Go release notes for the `go` version in `go.mod` (Go sets the macOS floor) |
| Windows 10/11, WebView2 runtime | Download, FAQ | Wails version in `go.mod`, its platform docs |
| Linux GTK 3 + WebKitGTK 4.1 package names | Download, FAQ | `-tags webkit2_41` and `apt-get install` line in `release.yml` |
| Builds come from CI, from a tagged release | Download | `release.yml` trigger |
| Not notarised / not code-signed, and the workaround | FAQ | Signing steps in `release.yml` (none today) |
| SHA-256 next to every download, how to verify | Download, FAQ | `digest` in `pkg/github`; `SHA256` in `internal/site/core/service.go` |
| Page title and meta description | `<head>` | The one-line description in `.agents/AGENTS.md` |
| README badges: Go and Wails versions, "1 direct" dependency | `README.md` | `go.mod` |
| Mascot credit (Go gopher, CC BY 4.0) and font licence | Footer, README | `img/`, `static/OFL-OpenSans.txt` |
| Maker credit: Plaasbotter | Footer, README | `author` in `cmd/bikasend/wails.json` |
| Copyright BikaSolutions (footer, README); MIT licence (README) | Footer, README | `companyName` in `cmd/bikasend/wails.json`; `LICENSE` |

A new claim on the site needs a new row here. A row whose source no longer exists means the claim
must go or change.
