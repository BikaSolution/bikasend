# Bikasend threat model

What we protect, who could attack it, and what stops them. Update this file whenever a change
crosses one of the trust boundaries below (`10-security.md`).

## Assets

| Asset | Where it lives |
| --- | --- |
| API keys, tokens, passwords | Desktop app memory only (session, prompts, AWS values, OAuth2 tokens) |
| AWS credentials | The user's aws CLI and its SSO cache; Bikasend asks the CLI per call |
| The user's files | Their disk; Bikasend reads spec files, `.bika.json`, chosen upload files |
| Release binaries | GitHub Releases; the site hands out short-lived download links |
| `GITHUB_TOKEN`, `TUNNEL_TOKEN` | Environment of the site container |

## Trust boundaries

```text
 untrusted                     │ Bikasend                         │ trusted by the user
───────────────────────────────┼──────────────────────────────────┼──────────────────────
 spec files in a cloned repo ──▶ core parser (os.Root, size caps) │
 .bika.json (anyone with   ──▶ off until trusted (sha256 pin) │
   push access writes it)      │                                  │
 HTTP responses ───────────────▶ Go: capped, redacted, validated  │
                               │   │ text only (textContent)      │
                               │   ▼                              │
                               │ webview ⇄ Go bridge (bound API) ◀─ the user
                               │ Go ─▶ aws CLI (no shell, args checked) ─▶ AWS
 internet ─▶ Cloudflare tunnel ─▶ site (rate limited, CSP) ─▶ GitHub API (token)
```

## Threats and mitigations

| # | Threat | Mitigation | Where |
| --- | --- | --- | --- |
| T1 | A hostile spec runs script in the webview (XSS), which can call Go | All untrusted text goes in with `textContent`; strict CSP (`script-src 'self'`, no inline); JS and CSS in files | `frontend/dist/app.js`, `index.html` |
| T2 | A spec reads files outside the opened folder (`$ref`, symlinks), or hidden files such as `.env` inside it | Every read goes through `os.Root`; external `$ref` must be a valid relative path to a visible `.json`, `.yaml` or `.yml` file; remote refs refused; `.bika.json` and its temp file are never followed as symlinks (a repo could plant one pointing at a git hook) | `core/swagger.go`, `core/service.go` |
| T3 | A huge, deeply nested or fan-out spec hangs or crashes the app | 20 MB file cap, 200 MB folder cap, YAML nesting limit (256), `$ref` chain limit, cycle detection, example node budgets, response-check budget, fuzz tests | `core/swagger.go`, `core/yaml.go`, `core/validate.go` |
| T4 | A committed `.bika.json` or a spec's server sends your secrets to an attacker's host | Folder trust with a sha256 pin; any outside change asks again; `.bika.json` read with a 1 MB cap; `hosts` per environment block secrets elsewhere; with no `hosts` list, only loopback or a host the user confirms on that send; OAuth2 needs a `hosts` list; no proxy in shared files | `core/service.go`, `core/http.go` |
| T5 | A secret leaks on a redirect | Requests carrying secrets do not follow redirects to another host or from https to http | `core/http.go` |
| T6 | A secret goes over plain http | Blocked unless the user confirms each send; loopback is exempt | `core/http.go` |
| T7 | A secret ends up on disk, in logs or in the webview | Values stay in Go memory; views mask raw and URL-escaped forms; reveal and copy are explicit (the hover card masks secrets until Reveal is clicked); history on disk masks secret-looking values, secret query values in URLs and free-text bodies; `guardFileValue` refuses secrets in shared files; the app never logs | `core/variables.go`, `core/history.go` |
| T8 | A server echoes a secret back | Responses are scanned for loaded secret values, raw and escaped, and redacted | `core/http.go` |
| T15 | A spec makes the app upload a local file (`/proc/self/environ`, `~/.aws/credentials`) | A spec never fills a file field; uploads accept only paths the user picked in the file dialog this session | `core/swagger.go`, `core/http.go`, `api/handlers.go` |
| T9 | Option injection into the aws CLI | No shell; IDs, profiles and regions match strict patterns that cannot start with `-` | `pkg/awscli` |
| T10 | Path traversal or open redirect on the download site | Downloads resolve only tag/name pairs from the release list; redirects go only to an https URL GitHub returned, on `github.com` or `*.githubusercontent.com`; the API base URL must be https | `internal/site`, `pkg/github` |
| T11 | XSS on the site through release names or notes | `html/template`; CSP `default-src 'none'` with no inline styles or scripts | `internal/site/api` |
| T19 | A forged `Host` header poisons links, the sitemap or a cache | Absolute URLs come only from the `-public-url` flag (https origin, checked at start-up); the feed and page links stay relative | `internal/site/api/routes.go` |
| T20 | The in-app update saves a tampered, older or misplaced file | Downloads only from the update site in the build, through redirects to that site, `github.com` or `*.githubusercontent.com`; only a newer tag, with a name that matches it; SHA-256 must match `latest.json`; 300 MB cap; temp file first, `os.Link` never overwrites. Accepted: a compromised update site can serve a matching hash, so users can also run `gh attestation verify` | `core/history.go` |
| T21 | A copied request (cURL, PowerShell, HTTPie, JavaScript, Python) runs code, reads local files or leaks a secret when pasted | Every piece quoted for its language, the method included; secrets become `BIKASEND_`-prefixed environment variables; the export is refused if a secret appears in any form (raw, URL-escaped, too short to replace), if it holds control or bidi characters, or if HTTPie would read a value as a file or separator; curl uses `--form-string` and `--globoff` | `core/curl.go` |
| T12 | Abuse of the public site or GitHub quota | Per-IP token bucket on pages, `/downloads` and `/latest.json` with O(1) eviction; release list cached for 5 minutes; signed download URLs reused for 1 minute; 1 minute back-off after a GitHub failure; Cloudflare WAF and rate rules at the edge | `pkg/httpserve/ratelimit.go`, `internal/site/core` |
| T16 | Denial of service on the site: slow clients, floods from many IPs, huge headers, log floods | Read, write, idle and header timeouts; 16 KB header cap; at most 256 requests in flight, then 503; logged paths capped; container memory, CPU, process and log limits | `pkg/httpserve`, `deploy/site/docker-compose.yml` |
| T17 | A spoofed `CF-Connecting-IP` dodges the rate limit | The header counts only with `-behind-cloudflare` and only from a private or loopback peer; the container publishes no ports | `pkg/httpserve/ratelimit.go`, `build/site/Dockerfile` |
| T18 | A file with secrets in the repo ends up in the site image | `.dockerignore` is an allowlist (site source only); the Dockerfile copies only `cmd/site`, `internal/site` and `pkg` | `.dockerignore`, `build/site/Dockerfile` |
| T13 | Tampered release binary | Built by CI from tags; SHA-256 shown per file; module list attached to each release; signed build provenance per file (`gh attestation verify`) | `.github/workflows/release.yml` |
| T14 | Compromised GitHub Action or dependency | Actions pinned to commit SHAs; one direct Go module; Dependabot; govulncheck, gosec, monthly audit | `.github/` |

## Accepted risks

- **Go strings cannot be wiped from memory.** A memory dump of a running app can contain secrets.
  Quitting the app frees them.
- **A trusted `.bika.json` is trusted.** Review it like code before you click Trust.
- **Builds are not signed or notarised yet.** Users must check the SHA-256. Planned under Distribution.
- **`CF-Connecting-IP` is trusted by the rate limiter** when the site runs with `-behind-cloudflare`
  and the peer is private. Safe only while the site container publishes no ports and is reachable
  through the tunnel alone.
- **Volumetric floods are Cloudflare's job.** The site's own limits protect the container, not the
  host's bandwidth. Keep the Cloudflare settings in the README on.
- **The aws CLI is trusted.** Bikasend runs whatever `aws` it finds; set its path in Settings
  if more than one is installed.
