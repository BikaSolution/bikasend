# Security (CRITICAL)

Security comes first. This rule overrides every other rule. When a feature and a security rule
conflict, the security rule wins, and you tell the human.

## Public repo

The repo is public. Before every commit, check the staged diff for secrets, tokens, private keys,
personal emails, internal hostnames and names of other companies, repos or services. Commits use the
GitHub noreply address. Examples use `example.com`, `example-service`, `Example API`.

## Restrictions

You have NO PERMISSION to access, read, modify, or reference:

- Any `.env` files (`.env`, `.env.local`, `.env.production`, etc.), in this repo or in a scanned folder
- Secrets or sensitive configuration data (API keys, tunnel tokens, signing keys)

Never suggest reading these files for any purpose.

The threat model (`docs/threat-model.md`) lists every trust boundary and what guards it. A change
that crosses a boundary updates it in the same PR.

## Desktop app

- **Every read stays in the folder.** Scans, `$ref` targets, `bikasend.json` and TLS files go
  through `os.Root`. The scan reads only `*swagger*` / `*openapi*` JSON and YAML files. Remote
  `$ref` is refused.
- **`bikasend.json` is untrusted until the user trusts it.** Trust pins the file's sha256; any
  change from outside the app asks again. Shared files never hold a proxy, absolute paths or
  secret values (`guardFileValue` refuses secret-looking names with plain text).
- **Secrets stay in Go memory.** Session values, prompts, AWS values and OAuth2 tokens are never
  logged, written to disk, put in `localStorage` or sent to the webview. Views are masked; reveal
  and copy are explicit clicks. History on disk is opt-in and masks secret-looking values.
- **Secret guards on every send:** the environment's `hosts` list (also for the OAuth2 token
  URL, which needs one); with no list, only loopback or a host the user confirms on that send; no
  secret over plain http without a confirmation; no redirect with a secret to another host or from
  https to http; loaded secrets are masked and redacted raw and URL-escaped. Do not weaken any of them.
- **Uploads come from the file dialog only.** A spec, cURL import or history entry never chooses a
  local file. `addFile` accepts only paths recorded by `PickFile` in this session.
- **The aws CLI is called without a shell.** Every ID, profile and region matches a pattern that
  cannot start with `-`. Never pass user text to `exec` any other way.
- **Untrusted text stays text.** Swagger content and HTTP responses go into the DOM with
  `textContent` or `replaceChildren`, never `innerHTML`, `outerHTML`, `insertAdjacentHTML`,
  `document.write` or `eval`. The webview can call Go, so injected script can read files and send
  requests. A breach of this is a **Blocker** in review.
- **No remote code in the webview.** No CDN scripts, no remote fonts, no iframes. Everything ships
  embedded. The CSP meta tag in `index.html` allows `'self'` only: no inline scripts or styles.
- **Bounded reads.** Responses (`maxBody`), specs (`maxSpec`), all specs in a folder (`maxScan`),
  uploads (`maxUpload`), `bikasend.json` (`maxConfig`) and aws CLI output are capped. Keep a cap on
  anything new.
- **Bounded work.** A small hostile file must not cost unbounded memory or time. YAML nesting
  (`maxYAMLDepth`), generated examples (`maxExampleNodes`, `maxExampleTotal`) and response checks
  (`maxChecks`) have limits. Every loop over untrusted input must consume input on each pass.
- **Bound methods are an API.** Every exported method on `api.App` is callable from JS. Add one only
  when the UI needs it, and validate its input in `core`.

## Download site

- Public once the tunnel runs. Treat every request as hostile.
- Downloads resolve only `tag/name` pairs found in the cached release list. A request value never
  becomes a URL or a path by itself.
- The site redirects to GitHub's signed asset URL (`pkg/github.AssetURL`). It does not follow the
  redirect with the token attached and does not proxy file bytes.
- Release names and notes come from GitHub and are untrusted. Render them through `html/template`
  as text, never `template.HTML`. Never `text/template` for HTML.
- `GITHUB_TOKEN` is a fine-grained token, read-only `Contents`, scoped to the bikasend repo only.
  It comes from the environment, never a flag (flags show in `ps`). Never log it; log
  `token_set` as a boolean at most.
- Outbound calls go to the configured GitHub API only, with a timeout and a size cap.
- Every response carries the security headers in `pkg/httpserve/middleware.go` (CSP, nosniff,
  frame deny, no-referrer, HSTS, COOP, CORP, Permissions-Policy), including the router's own 404
  and 405. Do not loosen the CSP: no inline styles or scripts, CSS lives in `static/site.css`.
- Pages, `/downloads/` and `/latest.json` are rate limited per client (`pkg/httpserve/ratelimit.go`).
  The client IP comes from `CF-Connecting-IP` only with `-behind-cloudflare` and only from a
  private peer. That is safe only while the container publishes no ports and is reached through
  the tunnel alone. Never add `ports:` to the compose file.
- Keep the DoS limits in `pkg/httpserve`: server timeouts, the header cap, the in-flight cap and
  capped log paths. Work per request stays O(1) under the limiter's lock.
- GitHub is a shared, rate-limited resource: keep the release cache, the failure back-off and the
  signed-URL cache. A visitor's request never triggers more than one GitHub call.
- Download redirects go only to `https` URLs on `github.com` or `*.githubusercontent.com`.
- Show a SHA-256 for every file so users can check what they run.
- Listens on `127.0.0.1` by default. Only the Docker image listens on `0.0.0.0`.
- The container runs as a non-root user on `scratch`, with a read-only root filesystem, no
  capabilities, `no-new-privileges`, and memory, CPU, process and log limits.
- `.dockerignore` is an allowlist. Add a path only when the site build needs it.
- Base images are pinned by digest; Dependabot bumps them.
- `TUNNEL_TOKEN` and `GITHUB_TOKEN` come from the shell environment. Never commit them, never
  write them to a file in the repo, never log them.

## CI and supply chain

- Pin every GitHub Action to a full commit SHA with the version in a comment. Dependabot bumps them.
- Workflows start with `permissions: {}` and grant the least each job needs.
- Pass `${{ }}` values to `run:` through `env:`, never inline. Tag names are user input.
- `actions/checkout` with `persist-credentials: false`. No shared Go cache in release builds.
- Pin tool versions (`wails@v2.17.1`), never `@latest`, in CI. `make security` is the exception: a
  scanner must be current.
- Dependabot (`.github/dependabot.yml`) watches Go modules, Actions and the Docker base image.
  Merge its security PRs first.

## Logging

- Log method and path. Never headers, query strings, request bodies or response bodies.
- A secret in a log, on disk, or in the repo is a **Blocker** in review.

## Security audits

Run an audit:

- before every PR,
- after every change to `go.mod`, `internal/site`, `pkg/httpserve`, `pkg/github`, `build/`,
  `deploy/`, `.github/`, or the frontend,
- at least once a month, even with no changes (new CVEs appear in old code).

An audit is:

1. `make security` — `govulncheck` (known CVEs in deps and stdlib) and `gosec` (insecure patterns).
   Fix every finding, or record why it does not apply with a `#nosec` comment that names the reason.
2. The `security-auditor` agent (`.agents/agents/security-auditor.md`) over the diff, or over the
   whole repo for the monthly audit.
3. Update Go and Wails when a fix lands: `go get -u github.com/wailsapp/wails/v2 && go mod tidy`,
   and bump `golang:` in `build/site/Dockerfile`.

Report the result to the human: what was checked, what was found, what was fixed.
