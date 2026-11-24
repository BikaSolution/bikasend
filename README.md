<p align="center">
  <img src="img/mascot.png" width="200" alt="The Bikasend gopher holding a paper plane">
</p>

<h1 align="center">Bikasend</h1>

<p align="center">
  <strong>Send requests straight from your Swagger docs.</strong><br>
  A lightweight desktop API client for services that already document themselves.<br>
  <a href="https://send.bikasolutions.co.za"><strong>Download Bikasend</strong></a> · free and open source
</p>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white" alt="Go 1.27"></a>
  <a href="https://wails.io"><img src="https://img.shields.io/badge/Wails-v2.17-DF0000" alt="Wails v2.17"></a>
  <img src="https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-0369a1" alt="macOS, Windows, Linux">
  <img src="https://img.shields.io/badge/OpenAPI-2.0%20%7C%203.x-6BA539?logo=openapiinitiative&logoColor=white" alt="Swagger 2.0 and OpenAPI 3">
  <a href=".github/workflows/release.yml"><img src="https://github.com/BikaSolution/bikasend/actions/workflows/release.yml/badge.svg" alt="Release build"></a>
  <br>
  <img src="https://img.shields.io/badge/dependencies-1%20direct-brightgreen" alt="One direct dependency">
  <a href=".github/dependabot.yml"><img src="https://img.shields.io/badge/dependabot-enabled-025E8C?logo=dependabot&logoColor=white" alt="Dependabot enabled"></a>
  <a href=".agents/rules/10-security.md"><img src="https://img.shields.io/badge/security-govulncheck%20%2B%20gosec-0b1622" alt="Scanned with govulncheck and gosec"></a>
  <img src="https://img.shields.io/badge/code%20style-gofmt-00ADD8" alt="gofmt">
  <img src="https://img.shields.io/badge/telemetry-none-555" alt="No telemetry">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT licence"></a>
</p>

---

Bikasend opens a repo of services, finds every Swagger and OpenAPI file in it (JSON or YAML),
and turns each operation into a ready-to-send request. There are no
collections to build, no accounts and no sync. It's like Postman or Bruno without the parts we
don't use.

## Features

- **Open a folder, get every endpoint.** Scans the repo for Swagger and OpenAPI files, grouped by
  tag. Filter by path or method, or paste a URL to find its operation. It skips `.git`,
  `node_modules` and `vendor`, and reloads when a spec changes.
- **Swagger 2.0 and OpenAPI 3.0 / 3.1**, JSON or YAML, with `$ref` across files and server
  variables.
- **Bodies pre-filled** from the schema's examples, enums and defaults. Path and query
  parameters get their own form. JSON, text, XML, form and multipart (file upload) bodies. The
  JSON editor colours the body and suggests the schema's keys and enum values as you type.
- **`{{variables}}` everywhere**, highlighted as you type, with autocomplete. Hover a variable to
  see where it comes from, reveal a secret with a click, or open it in the AWS console. Path
  params like `{id}` get their own colour, underlined until filled in; hover one to set it.
- **Environments and shared variables.** A committed `.bika.json` holds environments (local,
  staging…) and variable definitions for the whole team; Bikasend creates it the first time you
  save a shared variable. Create and delete environments and edit their hosts in the variables
  panel. Your own `globals.json` holds personal ones.
- **AWS secrets, loaded into memory.** Reference AWS Secrets Manager or Parameter Store values.
  Bikasend loads them through your own `aws` CLI and SSO login. Nothing secret is ever committed.
- **Auth helpers.** OAuth2 client credentials and AWS SigV4 signing per environment.
- **Responses that explain themselves.** JSON tree (copy any value or its path, like
  `$.items[0].name`), search, timing breakdown, image preview, a diff against the tab's previous
  response, and a check against the spec's response schema.
- **History, tabs, command palette (`⌘K`), cURL import.** Open tabs come back after a restart or
  an update, with secret-looking values masked; turn that off in Settings. Copy any request as
  cURL, PowerShell, HTTPie, JavaScript `fetch` or Python `requests`; secrets become environment
  variable references.
  `⌘ +` / `⌘ −` (`Ctrl` on Windows and Linux) change the text size.
- **Updates you can trust.** The version sits in the top bar and lights up when a new one is out. Click it and
  Bikasend downloads it for your platform and checks its SHA-256 against both the update site and
  the GitHub release. It shows you both hashes and asks before it replaces the app and restarts.
- **Secrets stay in memory.** Never written to disk, logs or the web view. Blocked from hosts an
  environment doesn't list unless you confirm the host, from plain http, and from redirects to
  another host or to plain http.
- **No CORS.** Requests run natively from Go, not from a browser.
- **Small.** About 10 MB, uses the system web view, works offline. The only call it makes on its
  own is an update check, which you can turn off in Settings.

## Variables, environments and AWS

Write `{{name}}` anywhere in a request. Values come from four scopes, the narrowest first:

| Scope | Lives in | Shared? |
| --- | --- | --- |
| Session | memory, gone when you quit | no |
| Environment | `.bika.json` → `environments.<name>.variables` | yes, through git |
| Folder | `.bika.json` → `variables` | yes, through git |
| Global | your own `globals.json` (Bikasend's config folder) | no |

`{{baseUrl}}` falls back to the spec's server. A committed `.bika.json` looks like this:

```json
{
  "variables": { "tenant": "demo" },
  "environments": {
    "local": {
      "hosts": ["localhost"],
      "variables": {
        "baseUrl": "http://localhost:8080",
        "X-API-Key": { "prompt": "Local API key" }
      }
    },
    "staging": {
      "hosts": ["*.staging.example.com"],
      "aws": { "profile": "staging", "region": "eu-west-1" },
      "variables": {
        "baseUrl": "https://api.staging.example.com",
        "X-API-Key": { "aws": "example-service/api-keys", "key": "bikasend" }
      }
    }
  }
}
```

The team shares where a secret lives, never the secret itself. Each person loads it with their own
AWS login (`aws sso login`, which Bikasend can start for you). Values are: a plain string,
`{"prompt": "…"}` (asked once per session), `{"aws": "<secret id>", "key": "<json key>"}`,
`{"ssm": "<parameter>"}` or `{"env": "<VARIABLE>"}`. Environments can also set `timeoutSeconds`,
`followRedirects`, `tls` (CA and client certificate files in the repo), `oauth2` and `sigv4`.

Bikasend ignores a folder's `.bika.json` until you trust it, and asks again whenever it changes.
Secrets are only sent to the hosts the environment lists. With no list, any other host needs your
confirmation (loopback is exempt): send once, or "Always allow" to add it to the environment's
hosts. `oauth2` needs a `hosts` list. Press `⌘E` to edit variables, environments and hosts.
The [docs page](https://send.bikasolutions.co.za/docs) lists every `.bika.json` field.

## Download

Get the latest build from the **[Bikasend download site](https://send.bikasolutions.co.za)** or
from this repo's [Releases](../../releases). Every build comes from CI and has a SHA-256 checksum.
From v0.1.4 on, each file also has signed build provenance:
`gh attestation verify <file> --repo BikaSolution/bikasend`.

| Platform | File | Needs |
| --- | --- | --- |
| macOS 13+ (Apple silicon and Intel) | `Bikasend-<tag>-macos-universal.zip` | — |
| Windows 10/11, 64-bit | `Bikasend-<tag>-windows-amd64.zip` | WebView2 runtime (built into Windows 11) |
| Linux, 64-bit | `Bikasend-<tag>-linux-amd64.zip` | `libgtk-3-0`, `libwebkit2gtk-4.1-0` |

Builds are not signed yet. On macOS, open the app once, then go to **System Settings → Privacy &
Security → Open Anyway**. On Windows, choose **More info → Run anyway**. Updates you install from
inside the app are checked against two SHA-256 hashes first (see above).

## Development

```sh
brew install go just
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1
wails doctor
```

| Task | Command |
| --- | --- |
| Run the app | `just run` |
| Run the app with live reload | `just dev` |
| Test | `just test` |
| Vet, lint, architecture rules | `just vet`, `just lint`, `just arch_lint` |
| Fuzz the spec parsers | `just fuzz` |
| Security scan (govulncheck + gosec) | `just security` |
| Build the app for this OS | `just app` → `build/bikasend/bin/` |
| Run the download site | `just site` → <http://localhost:8091> |
| Quick public URL for the site | `just tunnel` |

The only third-party Go module is Wails. Everything else is the standard library: no npm, no
frontend framework, no CDN.

## Project layout

```tree
cmd/bikasend/        Desktop app wiring + wails.json
cmd/site/            Download site wiring
internal/bikasend/   api (Wails bindings), core (scan, parse, variables, send), frontend
internal/site/       api (pages, feed, latest.json, downloads), core (cached GitHub releases)
pkg/sdk/             Service contracts: port.go, types.go, errors.go
pkg/awscli/          Runs your aws CLI: secrets, parameters, credentials, SSO login
pkg/sigv4/           AWS Signature V4, stdlib only
pkg/github/          GitHub Releases client
pkg/httpserve/       Stdlib router and rate limiter
docs/                Threat model
build/               App icon, Wails platform files, site Dockerfile
deploy/site/         docker-compose: site + Cloudflare tunnel
.agents/             Rules, skills and agents for AI coding tools
```

## Releasing

```sh
just release                  # shows the last release and suggests the next versions
just release v0.2.0           # tags; CI builds every platform into a draft release
just publish v0.2.0           # checks the build, signs the checksums, publishes
```

It checks that you are on an up-to-date, clean `main` and that the version is new, runs vet and
the tests, and asks before it pushes. It then sets the app's version in `wails.json` if needed
(one commit), and pushes the tag. Tags are protected: once pushed, only an admin can move or
delete one.

[`release.yml`](.github/workflows/release.yml) builds macOS, Windows and Linux on native
runners into a draft release with generated notes. If GitHub has no macOS runner free,
`retry-macos.yml` reruns that job, at most 3 times. `just publish`, run from `main` right after
`just release`, then checks that the draft holds exactly the expected files, that
`release.yml` built each one from the tag's commit (`gh attestation verify`) and that GitHub's
digests match, signs
`Bikasend-<tag>-SHA256SUMS` with the offline release key and publishes the release. The download
site picks it up within 5 minutes. The app installs an update only when that signature checks
out with the public key in [`cmd/bikasend/release.pub`](cmd/bikasend/release.pub).

Make the signing key once with `just keygen` (it asks for a passphrase), back up the key file and
the passphrase, and commit `cmd/bikasend/release.pub`.

Before tagging, check that the site still describes the app correctly
([`90-website-audit.md`](.agents/rules/90-website-audit.md)).

## Download site

The site lists the newest releases from GitHub and redirects each download to GitHub's
short-lived signed URL. It never stores or streams files.

```sh
TUNNEL_TOKEN=… GITHUB_TOKEN=… docker compose -f deploy/site/docker-compose.yml up -d --build
```

- `GITHUB_TOKEN`: a fine-grained token with read-only **Contents** on this repo only. Not needed
  if the repo is public.
- `TUNNEL_TOKEN`: from the Cloudflare tunnel. Route its public hostname to `http://site:8091`.
- `-public-url` (set in the compose file): the site's own address. Pages then carry canonical
  links and link previews, and `/sitemap.xml` exists. Submit it in Google Search Console.

The container is read-only, runs as a non-root user with no capabilities, and has memory, CPU,
process and log-size limits. It publishes no ports: only the tunnel can reach it.

**Uptime.** `GET /healthz` answers `ok` with `Cache-Control: no-store`. It makes no GitHub call
and shows no version. The image has no curl, so the compose health check runs the binary itself
(`/site -healthcheck`, a 3 s GET of `http://127.0.0.1:8091/healthz`) every 30 seconds. The tunnel
starts once the site is healthy, and `docker compose ps` shows the state. For a check from
outside, point a monitor at `https://send.bikasolutions.co.za/healthz` (or your own site's
address). Any uptime service with a free keyword monitor that looks for `ok` works, or a
Cloudflare health check if your plan has one. The monitor uses `GET` or `HEAD`, so the WAF rule
below lets it through.

The site limits each client, caps concurrent requests, times out slow clients and backs off when
GitHub is down. Cloudflare stops floods before they reach the tunnel. Set these in its dashboard:

- **SSL/TLS:** Full mode, Always Use HTTPS, HSTS, minimum TLS 1.2.
- **Security:** the free managed WAF ruleset on. Leave Bot Fight Mode off: it challenges the app's
  update check and feed readers, and the free plan cannot exempt a path from it.
- **WAF custom rule:** block every method except `GET` and `HEAD`.
- **Rate limiting rule:** `/downloads/` at about 20 requests per 10 seconds per IP, block for 10 seconds.
- **Tunnel:** one public hostname to `http://site:8091`, and a catch-all rule that returns 404.
- **DNS:** DNSSEC on.
- **During an attack:** turn on Under Attack mode.

## Security

Security comes first in this repo. Read [`10-security.md`](.agents/rules/10-security.md) before
you change anything. In short:

- read the [threat model](docs/threat-model.md) before you change a trust boundary,
- run `just security` and the `security-auditor` agent before every PR; CI scans every PR and a
  monthly audit opens an issue when something new turns up,
- secrets live in memory or in environment variables, never in files or logs,
- untrusted text (Swagger files, responses, release notes) is always rendered as text,
- GitHub Actions are pinned to commit SHAs, and Dependabot keeps them and Go up to date.

## Roadmap

Planned work lives in [ROADMAP.md](ROADMAP.md). Next up: distribution (code signing,
notarisation and installers).

## Contributing

Read [`.agents/AGENTS.md`](.agents/AGENTS.md) and the rules in [`.agents/rules/`](.agents/rules/).
They apply to people and AI tools alike. Fill in every section of the
[PR template](.github/pull_request_template.md), including Security and Website.

## Credits

- Made by **Plaasbotter** at **BikaSolutions**.
- The Go gopher was designed by [Renée French](https://reneefrench.blogspot.com/) and is licensed
  under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). The Bikasend mascot is based
  on it.
- [Open Sans](https://fonts.google.com/specimen/Open+Sans) © The Open Sans Project Authors,
  licensed under the [SIL Open Font License 1.1](internal/site/api/static/OFL-OpenSans.txt). It's
  free to use, change and ship.
- The paper-plane app icon and favicon are original to Bikasend.
- Built with [Wails](https://wails.io).

## Licence

Bikasend is free and open source under the [MIT licence](LICENSE), © 2026 BikaSolutions. The
Open Sans font and the gopher artwork keep their own licences (see Credits).
