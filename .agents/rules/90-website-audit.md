# Website Content Audit

The download site (`internal/site/api/templates/index.html` and `docs.html`) and `README.md` make
promises about the app: features, formats, requirements, install steps. Most of that text is static. Releases come from
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
| `{{variables}}`, environments, a shared `.bika.json` | Features, How it works | `core/variables.go`, `core/service.go` |
| AWS Secrets Manager and Parameter Store through the user's `aws` CLI | Features, FAQ | `pkg/awscli`; `LoadSecrets` in `core/http.go` |
| Secrets only to listed hosts, not over plain http, not along cross-host redirects | Features | `guard` and `CheckRedirect` in `core/http.go` |
| Refuses secret-looking values in `.bika.json` | FAQ | `guardFileValue` in `core/variables.go` |
| JSON tree, search, timing, images, spec check; copy as cURL, PowerShell, HTTPie, JavaScript or Python, secrets as environment variables | Features | `app.js` `renderBody`; `core/validate.go`; `exporters` in `core/curl.go` |
| Environments and hosts editor, "Always allow" for a host | README | `envPanel`, `send` in `app.js`; `CreateEnvironment`, `SetHosts` in `core/service.go` |
| Version in the top bar, lights up for an update; update check at start (can be turned off), checked against the update site, GitHub and the release signature, installed on a click | Features, FAQ, README | `renderVersion`, `versionClick`, `updateDialog` in `app.js`; `CheckUpdates` default in `core/service.go`; `CheckUpdate`, `DownloadUpdate`, `githubDigest`, `checkSignature`, `InstallUpdate` in `core/history.go`; `pkg/sign`; `-X main.repo` in `release.yml` |
| Path params highlighted, set from a hover card | README | `fillHighlight`, `showParamCard` in `app.js` |
| The hero screenshot shows the current app | Hero | `static/app.webp`: retake it (generic example repo) when the UI changes |
| Bodies pre-filled from examples, enums, defaults | Features | `example` in `core/swagger.go` |
| Path, query, header params and auth headers set up | Features | `operation` in `core/swagger.go` |
| Secrets stay in memory, never on disk or in logs | Features | `store` in `app.js` (no secrets); `scrubRequest` in `core/history.go`; `10-security.md` |
| No CORS: requests run natively | Features | `Send` in `core/http.go` |
| About 10 MB, offline, no account, no telemetry; the only own call is the update check | Features, README | `just app` output size; `api()` calls in `app.js`; `CheckUpdate`, `latestRelease` in `core/history.go` |
| Remembers the folder; base URL per spec | How it works | `store` keys `server:`, `spec:` in `app.js`; `Recent` in `core/service.go` |
| Filter by path or summary, toggle methods, paste a URL | How it works | `renderOps`, `pathMatches` and the method filter in `renderSide` (`app.js`) |
| `⌘ Enter` / `Ctrl Enter` sends; status, time, size, headers, pretty JSON | How it works | the `keydown` handler in `wire` and `send` in `app.js`; `Send` in `core/http.go` |
| Platforms offered and architectures ("Universal", "64-bit") | Download | `matrix` in `.github/workflows/release.yml` |
| Asset names map to macOS / Windows / Linux | Download | `out=` in `release.yml`; `platform` in `internal/site/core/service.go` |
| macOS minimum version | Download | Go release notes for the `go` version in `go.mod` (Go sets the macOS floor); `LSMinimumSystemVersion` in `build/bikasend/darwin/Info.plist` must match |
| Windows 10/11, WebView2 runtime | Download, FAQ | Wails version in `go.mod`, its platform docs |
| Linux GTK 3 + WebKitGTK 4.1 package names | Download, FAQ | `-tags webkit2_41` and `apt-get install` line in `release.yml` |
| Builds come from CI, from a tagged release | Download | `release.yml` trigger |
| Not notarised / not code-signed, and the workaround | FAQ | Signing steps in `release.yml` (none today) |
| SHA-256 next to every download, how to verify | Download, FAQ | `digest` in `pkg/github`; `SHA256` in `internal/site/core/service.go` |
| Signed build provenance, `gh attestation verify` from v0.1.4 | FAQ | `Attest build provenance` step in `release.yml` |
| Page title and meta description, also in link previews | `<head>`, `static/og.jpg` | The one-line description in `.agents/AGENTS.md`; `indexTitle`/`indexDescription` in `internal/site/api/handlers.go` |
| Structured data for search engines: free, MIT, platforms, latest version, repo | `<head>` (`application/ld+json`) | `index.html`; `indexDescription` in `internal/site/api/handlers.go`; `LICENSE`; the Download rows above |
| Source on GitHub, MIT licence links | Header, hero, footer | `-repo` flag in `cmd/site`; `LICENSE` |
| README badges: Go and Wails versions, "1 direct" dependency | `README.md` | `go.mod` |
| Mascot credit (Go gopher, CC BY 4.0) and font licence | Footer, README | `img/`, `static/OFL-OpenSans.txt` |
| Maker credit: Plaasbotter | Footer, README | `author` in `cmd/bikasend/wails.json` |
| Copyright BikaSolutions; MIT licence | Footer, README | `companyName` in `cmd/bikasend/wails.json`; `LICENSE` |

### Docs page (`/docs`)

The docs page goes deeper than the index, so it goes stale faster. Check every row on each audit.

| Claim on `/docs` | Section | Source of truth |
| --- | --- | --- |
| First-run steps for macOS, Windows, Linux; SHA-256 commands | Getting started | The FAQ in `index.html` (keep both the same); signing steps and `apt-get` line in `release.yml` |
| Open folder, grouped by tag, reopens the last folder; trust prompt | Getting started | `welcome`, `start`, `renderOps`, trust dialog in `app.js`; `Open` in `core/service.go` |
| Filter by path, summary or pasted URL; send again cancels | Getting started | `renderOps`, `send` in `app.js` |
| Name has `swagger`/`openapi`, `.json`/`.yaml`/`.yml`, any case; needs a `swagger` or `openapi` field | Which files | `isSpecName`, `parse` (`ErrNotSpec`) in `core/swagger.go` |
| Skips `.git`, `node_modules`, `vendor`; YAML twin skipped | Which files | `scanSpecs` in `core/swagger.go` |
| `$ref` relative, visible `.json`/`.yaml`/`.yml`, remote refused, never outside the folder | Which files | `deref`, `refTarget` in `core/swagger.go`; `os.OpenRoot` in `core/service.go` |
| YAML subset: no anchors, tags or multi-document files | Which files | `core/yaml.go` |
| 20 MB per file, 200 MB per folder; one bad file fails only itself | Which files | `maxSpec`, `maxScan`, `scanSpecs` in `core/swagger.go` |
| Reloads on change of known files; new files on rescan (↻) | Which files | `Changed`, `fingerprint` in `core/service.go`; `watch` in `api/handlers.go`; `rescan` in `index.html` |
| What else it reads from the folder | Which files | `loadFolder`, `loadTLS`, `addFile`/`AllowUpload` in `core/` |
| Four scopes, narrowest first; `{{baseUrl}}` from the spec server, picker per spec | Variables | `lookup`, `definitions` in `core/service.go`; `serverFor` in `app.js` |
| `globals.json` in the user config folder, variables only | Variables, .bika.json | `ConfigDir` in `cmd/bikasend/main.go`; `parseConfig` in `core/variables.go` |
| Nesting up to 10, cycles refused, `{{{{text}}}}` literal, name pattern | Variables | `walk`, `varRef`, `namePattern` in `core/variables.go` |
| Every `.bika.json` field, its limits and defaults, unknown fields refused | .bika.json | `configFile`, `environment`, `parseConfig`, `validHost` in `core/variables.go`; `client`, `defaultTimout` in `core/http.go` |
| Created with a `local` environment on the first shared save | .bika.json | `editFile`, `starterConfig` in `core/service.go` |
| Trust pins the SHA-256, own edits keep it; 1 MB cap; symlink refused | .bika.json | `trusted`, `loadFolder`, `editFile` in `core/service.go` |
| `local` picked first; `prod…` asks before a send | .bika.json | `firstEnv`, `workspace` (`Production`) in `core/service.go`; `send` in `app.js` |
| OAuth2: https token URL, host in `hosts`, clientSecret only as a variable, token kept until it expires | .bika.json | `oauthToken` in `core/http.go`; `parseConfig` in `core/variables.go` |
| SigV4: `service` required, region falls back to `aws` | .bika.json | `parseConfig` in `core/variables.go`; `signV4` in `core/http.go` |
| Variable forms, one source each, non-text is secret | .bika.json | `varValue.UnmarshalJSON`, `isSecretKind` in `core/variables.go` |
| Secret-looking names and their exceptions | .bika.json | `secretName`, `guardFileValue`, `isOnlyReference` in `core/variables.go` |
| No proxy in the file; proxy is a setting | .bika.json | `configFile` (no field); `Settings.Proxy` in `pkg/sdk/bikasend/types.go` |
| AWS values load through the CLI, forgotten on environment switch | Secrets | `LoadSecrets`, `SignIn` in `core/http.go`; `switchEnv` in `core/service.go`; `pkg/awscli` |
| Masked until Reveal; history in memory unless saved, masked on disk; echoes masked | Secrets | `app.js` var card and history text; `saveHistory` in `core/history.go`; `mask` in `core/variables.go` |
| Hosts list, loopback exemption, Send once / Always allow, plain http, redirects | Secrets | `guard`, `CheckRedirect` in `core/http.go`; `isLoopback` in `core/variables.go`; `send` in `app.js` |
| Five export formats, `BIKASEND_` variables, refusals, auth notes | Copy as code | `exporters`, `Export`, `envVarName`, `unsafeRune`, `exportNotes` in `core/curl.go`; `EXPORTS` in `app.js` |
| Update check on start, off in Settings; saved to Downloads; two SHA-256 checks and the release signature; delete when any fails; install and restart | Updates | `CheckUpdate`, `DownloadUpdate`, `downloadDir`, `checkSignature`, `InstallUpdate` in `core/history.go`; `start`, `updateDialog` in `app.js` |
| Keyboard shortcuts | Keyboard shortcuts | `keydown` handler in `wire` and the `welcome` text in `app.js` |
| `/healthz` and the Docker health check | README (Download site) | `AddGenericHealthCheck`, `CheckHealth` in `pkg/httpserve/server.go`; `deploy/site/docker-compose.yml` |

A new claim on the site needs a new row here. A row whose source no longer exists means the claim
must go or change.
