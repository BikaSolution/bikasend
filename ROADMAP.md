# Bikasend Roadmap

What Bikasend could become, in priority order. The top items are designed in enough detail to
start work. The backlog lists everything else worth doing, with priority, effort and its impact on
dependencies.

Everything here follows the repo rules: **security first** (`10-security.md`), **Wails is the only
third-party module** (`00-repo.md`), and **the existing repo layout**. When an
item would break one of those, it says so.

> **Status (9 Nov 2026):** everything in sections 1 and 2 is built, except Distribution, code
> signing and the frontend smoke test (⏳). ✅ done · ◐ partly done · ⏳ not started.
> The download site and README describe shipped features only (`90-website-audit.md`).

---

## Contents

1. [Must-haves](#1-must-haves)
   - [1.1 Variables: `{{api_key}}` everywhere](#11-variables-api_key-everywhere)
   - [1.2 Shared and global variables](#12-shared-and-global-variables)
   - [1.3 AWS Secrets Manager, loaded into memory](#13-aws-secrets-manager-loaded-into-memory)
   - [1.4 An easy variable editor](#14-an-easy-variable-editor)
   - [1.5 Delivery plan](#15-delivery-plan)
   - [1.6 What changed from the design](#16-what-changed-from-the-design)
2. [Backlog](#2-backlog)
3. [Out of scope](#3-out-of-scope)
4. [Open questions](#4-open-questions)

Legend: **P1** next up · **P2** soon · **P3** nice to have.
Effort: **S** ≤ 1 day · **M** a few days · **L** a week or more.
Deps: ✅ stdlib only · ⚠️ needs a new dependency (needs the human's OK).

---

## 1. Must-haves

### 1.1 Variables: `{{api_key}}` everywhere

Write `{{name}}` in the URL, headers or body, like Postman and Bruno. Bikasend fills in the value
when you press Send.

```text
GET {{baseUrl}}/items/{{itemId}}
Authorization: Bearer {{token}}
X-API-Key: {{api_key}}
```

**Rules**

- Syntax: `{{name}}`. Names are `[A-Za-z0-9_.-]+`. `{{ name }}` with spaces also works.
- Resolution happens **in Go, inside `Send`**, never in the webview. The frontend sends the
  template, and Go resolves it and sends the request. Secret values never reach JavaScript unless
  you click "reveal".
- A value may contain other variables (`{{baseUrl}}` = `https://{{host}}/v1`). Depth limit 10, and
  a cycle is an error naming the loop (`a → b → a`).
- A missing variable blocks Send with a clear message: `{{tenant}} is not defined in any scope`.
  Never send a literal `{{tenant}}` to a server.
- Swagger operations pre-fill with variables, not hard-coded values. The base URL becomes
  `{{baseUrl}}`, and an API-key header becomes `X-API-Key: {{X-API-Key}}`, or whatever name the
  folder maps it to.
- Escape hatch: `{{{{literal}}}}` sends `{{literal}}`.

**Where the code goes**

| File | What |
| --- | --- |
| `pkg/sdk/bikasend/types.go` | `Variable`, `Scope`, `Environment`, `VariableView` (masked, for the UI) |
| `pkg/sdk/bikasend/port.go` | `Variables()`, `SetVariable()`, `SelectEnvironment()`, `LoadSecrets()` |
| `pkg/sdk/bikasend/errors.go` | `ErrUndefinedVariable`, `ErrVariableCycle`, `ErrSecretHost` |
| `internal/bikasend/core/variables.go` | Parse scopes, merge, resolve, detect cycles |
| `internal/bikasend/core/service.go` | `Send` resolves before building the request |
| `internal/bikasend/api/handlers.go` | Thin bound methods for the new port calls |

All stdlib: `regexp`, `encoding/json`, `os.UserConfigDir`. ✅

---

### 1.2 Shared and global variables

Four scopes. A narrower scope wins over a wider one:

```text
Session  >  Environment  >  Folder  >  Global
(memory)    (folder file)   (folder file)  (your machine)
```

| Scope | Where it lives | Shared with the team? | Holds secrets? | Example |
| --- | --- | --- | --- | --- |
| **Global** | `<user config dir>/Bikasend/globals.json` | No, it's yours | References only | `{{myName}}`, a personal AWS profile |
| **Folder** | `.bika.json` in the repo root, committed | **Yes**, via git | References only | `{{tenant}}`, AWS secret references |
| **Environment** | `environments` block inside `.bika.json` | **Yes** | References only | `{{baseUrl}}` per local / staging |
| **Session** | Memory only, gone on quit | No | **Yes** (typed or loaded) | A token you pasted, loaded AWS values |

**How shared secrets work:** the team shares **where** a secret lives, never the secret. The
committed `.bika.json` holds references (`aws:example-service/api-keys#bikasend`). Each person's
Bikasend resolves them with their **own** AWS login, so access stays governed by IAM. Nobody pastes
keys into Slack and nothing secret lands in git.

**`.bika.json` (committed, safe to share)**

```json
{
  "variables": {
    "tenant": "demo",
    "itemId": "42"
  },
  "environments": {
    "local": {
      "variables": {
        "baseUrl": "http://localhost:8080",
        "api_key": { "prompt": "Local API key" }
      }
    },
    "staging": {
      "hosts": ["*.staging.example.com"],
      "aws": { "profile": "staging", "region": "eu-west-1" },
      "variables": {
        "baseUrl": "https://api.staging.example.com",
        "api_key": { "aws": "example-service/api-keys", "key": "bikasend" },
        "db_note": { "aws": "example-service/readme" }
      }
    }
  }
}
```

A variable value is one of:

| Form | Meaning |
| --- | --- |
| `"text"` | Plain value. **Never put a secret here.** It's committed. |
| `{ "prompt": "Label" }` | Ask for the value once per session and keep it in memory |
| `{ "aws": "<secret-id>" }` | The whole `SecretString` |
| `{ "aws": "<secret-id>", "key": "<json-key>" }` | One field of a JSON secret (the usual case) |
| `{ "env": "NAME" }` | Read from an environment variable at start-up (useful for CI and scripts) |

**Global file** (`globals.json`, per user, never committed): the same shape, for variables you
want in every folder. On macOS that's `~/Library/Application Support/Bikasend/`, on Windows
`%AppData%\Bikasend\`, on Linux `~/.config/Bikasend/`.

**Guards**

- Bikasend refuses to **save** a plain-text value whose name looks secret (`*key*`, `*token*`,
  `*secret*`, `*password*`) to a file. It offers "keep in session" or "reference AWS" instead.
- `hosts` binds an environment's secrets to its hosts. If a resolved request carries a secret to
  a host outside the list, Send is blocked (`ErrSecretHost`). That stops a staging key from going
  to production, or to a typo'd host, by accident.
- A secret sent over plain `http://` to anything but `localhost` shows a warning the user must
  confirm.
- Files are read through `os.Root` (as `scan` does), with a size cap and strict JSON
  (`DisallowUnknownFields`), so a typo fails loudly.

---

### 1.3 AWS Secrets Manager, loaded into memory

**Approach: call the AWS CLI, not the AWS SDK.**

| | AWS CLI (`aws`) | AWS SDK for Go v2 | Hand-written SigV4 |
| --- | --- | --- | --- |
| New Go modules | **0** ✅ | ~15 ⚠️ | 0 ✅ |
| SSO, profiles, MFA, `credential_process` | All, for free | Most | We build each one ourselves |
| Matches how devs already log in | Yes (`aws sso login`) | Partly | No |
| Works without the CLI installed | No | Yes | Yes |
| Effort | **S–M** | M | L |

Start with the CLI. Most developers already have it set up, and it gives us SSO, profiles and MFA
for free. Add SigV4 later only if machines without the CLI need it (see backlog).

**Flow**

```text
Pick environment "staging"
  └─ aws sts get-caller-identity --profile staging      ← logged in?
       ├─ no  → show "Sign in to AWS (staging)" → aws sso login --profile staging (opens browser)
       └─ yes → aws secretsmanager batch-get-secret-value \
                  --secret-id-list example-service/api-keys example-service/readme \
                  --profile staging --region eu-west-1 --output json
                → parse SecretString → pick "key" fields → keep in memory
                → status: "AWS staging · 2 secrets · loaded 14:02"
```

**Implementation rules**

- New package `pkg/awscli` (`client.go`, `types.go`, `errors.go`, `client_test.go`), a thin
  wrapper around `exec.CommandContext`, the same shape as `pkg/github`.
- Arguments go in as a slice, **never through a shell**. Validate secret IDs, profile and region
  against strict patterns before running.
- Environment for the child process: `AWS_PAGER=""`, `AWS_CLI_AUTO_PROMPT=off`,
  `NO_COLOR=1`. Timeout of 30 s via context.
- Never log stdout. Keep stderr only for a short error message (`ExpiredToken`,
  `AccessDeniedException`, `ResourceNotFoundException`), mapped to a clear sentence in the UI.
- Values live in memory only, in the session scope. **Reload** fetches again, and **Forget**
  clears them. Quitting clears everything.
- Use `batch-get-secret-value` (up to 20 IDs per call) to cut round trips. Fall back to
  `get-secret-value` per ID if the batch call is denied by IAM.
- A secret that isn't JSON and has no `key` uses the whole string. A `key` that's missing is an
  error that names the secret and the key, but never the value.
- Go strings can't be reliably wiped from memory. Accept that, keep values out of logs, files and
  the webview, and say so in `10-security.md`.

**Tests** (stdlib only, no fixtures): use the helper-process pattern. The test binary re-runs
itself as a fake `aws` (`os.Args[0]` with `GO_WANT_HELPER_PROCESS=1`), with the canned JSON
inline in `client_test.go`. Cover: logged in, expired SSO, access denied, JSON key pick, non-JSON
secret, batch with a missing ID.

**Later (P2/P3):** SSM Parameter Store (`aws ssm get-parameters --with-decryption`), with
references written as `{ "ssm": "/example/api-key" }`. A secret picker that lists secret names
(`list-secrets`, names only) with search.

---

### 1.4 An easy variable editor

**Top bar**

```text
┌──────────────────────────────────────────────────────────────────────────────────┐
│ [Open folder]  example-repo                 Env: [ staging ▾ ]   ● AWS staging ✓  │
│                                                      2 secrets · 14:02   [Vars ⌘E] │
└──────────────────────────────────────────────────────────────────────────────────┘
```

- **Environment switcher**: a dropdown with `local` / `staging` / …, and a coloured dot per
  environment (green local, amber staging, red anything named `prod*`).
- **AWS pill**: signed out, loading, loaded with count and time, or error with the reason. Click it
  to Reload, Forget or Sign in.

**Variables drawer** (`⌘E` / `Ctrl+E`), slides in from the right:

```text
┌ Variables ─────────────────────────────── [ Search… ] ───────┐
│ Session · Environment: staging · Folder · Global              │
├──────────────┬───────────────────────────┬─────────┬─────────┤
│ Name         │ Value                     │ Source  │         │
├──────────────┼───────────────────────────┼─────────┼─────────┤
│ baseUrl      │ https://api.staging.exa…  │ env     │ ✎       │
│ api_key  🔒  │ ••••••••••  👁  ⧉         │ AWS     │ ↻       │
│ tenant       │ demo   (overrides Global) │ folder  │ ✎       │
│ token    🔒  │ ••••••••••  👁  ⧉         │ session │ ✎ ✕     │
│ itemId   ⚠   │ — not defined —           │         │ + add   │
├──────────────┴───────────────────────────┴─────────┴─────────┤
│ + Add variable          Used in this request: baseUrl, api_key│
└───────────────────────────────────────────────────────────────┘
```

- One table, with tabs to filter by scope. "All" shows the **effective** value and which scope it
  came from.
- 🔒 means secret: masked by default. 👁 asks Go for the value. ⧉ copies via the Wails clipboard,
  so the value doesn't need to be shown.
- Editing a folder or environment row writes `.bika.json` (pretty-printed, keys sorted, so git
  diffs stay small). Secret-looking names are guarded as in 1.2.
- "Add variable" asks for the type in plain words: **Text**, **Ask me each session**,
  **From AWS Secrets Manager**, **From an environment variable**.
- Shadowing is shown inline ("overrides Global"), so nobody wonders which value wins.

**In the request editor**

- Typing `{{` opens autocomplete with every defined name, its scope and a masked preview.
- Variables are highlighted: green when resolved, 🔒 when secret, red wavy underline when
  undefined. Hover shows the source and a masked value. `⌘`-click opens it in the drawer.
- A **Preview** toggle shows the request exactly as it will be sent, with secrets masked.
- Send stays disabled while any variable is undefined, and the button says why.

**Accessibility:** everything works by keyboard, the drawer has focus management, and colour is
never the only signal (icons and text back it up).

---

### 1.5 Delivery plan

Each milestone ships on its own, behind no flags, and gets a website audit (`90-website-audit.md`).

| Milestone | Scope | Done when | Effort |
| --- | --- | --- | --- |
| ✅ **M1 Variables core** | `{{var}}` in URL, headers and body; Session scope; resolution in Go; undefined blocks Send; Preview | Tests cover nesting, cycles, missing names, escapes; the UI highlights variables | M |
| ✅ **M2 Shared variables** | `.bika.json` with Folder and Environment scopes; environment switcher; Swagger pre-fill uses `{{baseUrl}}` | Opening a repo with a `.bika.json` switches local ↔ staging with no edits | M |
| ✅ **M3 AWS secrets** | `pkg/awscli`; `{ "aws": … }` references; SSO sign-in prompt; batch load; AWS pill; Reload / Forget | A teammate clones the repo, signs in with their own profile, and staging calls work. No secret in git, logs or `localStorage` | M |
| ✅ **M4 Editor** | Variables drawer, autocomplete, hover, ⌘-click, `prompt` variables | Every action works by keyboard; light and dark checked | M |
| ✅ **M5 Global + guards** | `globals.json`; host binding; plain-HTTP warning; secret-name save guard | Sending a staging secret to another host is blocked, with a test | S |

Update `10-security.md` with the new secret rules in the same change as M1.

### 1.6 What changed from the design

- **Folder trust.** A cloned repo's `.bika.json` decides which hosts get your secrets, so it
  stays off until you trust it. Trust pins the file's sha256; any change from outside asks again.
- **No proxy in shared files.** A committed file could route all traffic through an attacker, so
  the proxy is a personal setting only. TLS files must be paths inside the repo.
- **Secrets skip cross-host redirects.** Go strips `Authorization` on a redirect to another host,
  but not custom headers such as `X-API-Key`.
- **Hover on variables** became a chip row under the request: each variable used, its source and
  status, one click to edit or define it. A textarea cannot show hover cards.
- **Production guard** (open question 5): environments named `prod*` ask before every send.

---

## 2. Backlog

### Requests

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ✅ Path and query parameter fields | Fill `{id}` and `?page=` in a small form instead of editing the URL | P1 | S | ✅ |
| ✅ Cancel a running request | Long or hung calls need a Stop button (`context.CancelFunc`) | P1 | S | ✅ |
| ✅ Timing breakdown | DNS, connect, TLS, first byte via `net/http/httptrace` | P2 | S | ✅ |
| ✅ Request timeout and redirect settings | Per environment: timeout, follow redirects on or off | P2 | S | ✅ |
| ✅ Body types: form, multipart, file upload, raw text/XML | Not every API is JSON | P2 | M | ✅ |
| ✅ Custom CA and client certificates (mTLS) | Internal services behind private CAs | P2 | M | ✅ |
| ✅ HTTP proxy setting | Corporate networks (`http.ProxyFromEnvironment` already works; add a UI override) | P3 | S | ✅ |
| ✅ Cookie jar per session | Session-based APIs (`net/http/cookiejar`) | P3 | S | ✅ |
| ✅ AWS SigV4 request signing | Call IAM-protected API Gateway endpoints with your profile | P3 | M | ✅ (via CLI creds + stdlib HMAC) |

### Responses

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ✅ Search in the response body | Find a field in a big JSON reply | P1 | S | ✅ |
| ✅ Collapsible JSON tree | Read nested responses quickly | P2 | M | ✅ |
| ✅ Copy as cURL / import from cURL | Share a call with someone who doesn't use Bikasend | P1 | S | ✅ |
| ✅ Validate the response against the spec | Flag fields the spec doesn't promise, or required fields that are missing | P2 | M | ✅ |
| ✅ Save response to file, image preview | Downloads, PDFs (receipts), images | P3 | S | ✅ |
| ✅ Redact known secret values in responses | Some APIs echo the key back; mask any loaded secret found in the body | P2 | S | ✅ |

### Swagger and OpenAPI

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ✅ `$ref` for parameters, request bodies and responses | Hand-written specs use them a lot | P1 | S | ✅ |
| ✅ Multiple `servers` and server variables | Choose a server from a dropdown | P2 | S | ✅ |
| ✅ Group operations by tag, collapsible | Large specs have 50+ operations | P1 | S | ✅ |
| ✅ Show response schemas and examples | See what you'll get before you send | P2 | M | ✅ |
| ✅ Deprecated badge, required-param markers | Fewer surprises | P2 | S | ✅ |
| ✅ Watch the folder and reload changed specs | Regenerate swagger and see it right away (poll mtimes; no fsnotify) | P2 | S | ✅ |
| ✅ OAuth2 client-credentials helper | Fetch and cache a token per environment, in memory | P2 | M | ✅ |
| ✅ YAML specs | Repos that ship YAML only. Options: tiny YAML subset parser (✅, risky) or a YAML module (⚠️) | P3 | M | ⚠️ / ✅ |
| ✅ External `$ref` to other files | Split specs | P3 | M | ✅ |

### App experience

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ✅ Multiple request tabs | Compare two calls side by side | P2 | M | ✅ |
| ✅ History of sent requests (memory, optional file without secrets) | Re-run what you did a minute ago | P1 | M | ✅ |
| ✅ Resizable panes, font size, theme toggle | Comfort on small and large screens | P2 | S | ✅ |
| ✅ Command palette (`⌘K`) | Jump to an operation, environment or action | P2 | M | ✅ |
| ✅ Recent folders | Switch between repos quickly | P1 | S | ✅ |
| ✅ Update check | Compare with the site's latest tag, show "update available", verify SHA-256 | P2 | S | ✅ |
| ✅ Empty and error states with next steps | "No swagger files found: is this the repo root?" | P1 | S | ✅ |
| ✅ Secret hover card | See where a `{{secret}}` comes from, reveal it, jump to it in AWS | P2 | M | ✅ |
| ✅ Create the config file automatically | No "Create .bika.json" step before the first shared variable | P2 | S | ✅ |

**Secret hover card.** Hovering a `{{variable}}` chip opens a small card:

- Where the value comes from: scope (session, environment, folder, global), kind and reference, such as
  `aws: example-service/api-keys → key bikasend` with profile and region, `ssm: /example/api-key`,
  `env: EXAMPLE_API_KEY` or `prompt`, and when it was loaded.
- The value stays masked. A **Reveal** button shows it (the existing `Reveal`, an explicit click) and
  hides it again after 10 seconds. Showing it on hover is out: one stray mouse move during a screen
  share would leak it (`10-security.md`: reveal and copy are explicit clicks).
- **Open in AWS console** for `aws` and `ssm` references. Go builds the console URL from the stored,
  already-validated secret ID and region, and opens it through a new bound method that takes the
  variable name, never a URL from the webview. Only `https://<region>.console.aws.amazon.com/` URLs.
  Check the console URL formats for Secrets Manager and Parameter Store when this is built.
- **Edit** opens the variables drawer at the row that defines the value.

**Create the config file automatically.** Today the user clicks "Create .bika.json". Instead,
create it the first time someone saves a folder or environment variable, or picks "New environment",
with the same starter content as `CreateConfig` and trust pinned to the new file's hash (the app
wrote it). Do not create it just because a folder was opened: an unasked-for file in someone's repo
shows up in `git status` and gets committed by accident. The file becomes `.bika.json` (open
question 3, decided). No migration: nobody uses `.bika.json` in production yet, so the old name
is simply dropped. The site, README, rules and threat model change with it.

### Security

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ⏳ macOS notarisation and Windows code signing | Remove the "unverified developer" steps; users can trust the binary | P1 | M | ✅ (CI secrets + Apple / signing cert) |
| ✅ Module list and build provenance on every release | Know what's inside; prove CI built it (`Bikasend-<tag>-modules.txt` from `go version -m`; signed attestations, `gh attestation verify`). A formal SPDX/CycloneDX SBOM would need a generator tool. | P2 | S | ✅ |
| ✅ Fuzz tests for the Swagger parser | Untrusted files from any repo (`go test -fuzz`) | P2 | S | ✅ |
| ✅ Content-Security-Policy in the app webview | Same idea as the site, as defence in depth | P2 | S | ✅ |
| ✅ Monthly scheduled audit | GitHub Action on a cron: `make security` plus an issue when it fails | P2 | S | ✅ |
| ✅ Threat model doc | Webview ↔ Go bridge, secrets, the download site, CI | P2 | S | ✅ |

### Engineering

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ✅ CI on pull requests | `go vet`, `go test`, `make security` on every PR, not only on tags | P1 | S | ✅ |
| ✅ `golangci-lint` config with depguard boundaries | Enforce the import boundaries | P2 | S | ✅ (dev tool) |
| ✅ `go-arch-lint` config | Enforce the `cmd` / `internal` / `pkg/sdk` layering | P2 | S | ✅ (dev tool) |
| ✅ Pre-commit hooks | Lint, arch lint, tests before every commit | P2 | S | ✅ (dev tool) |
| ⏳ Frontend smoke test | Load `index.html` in a headless webview or browser and click through scan → send | P3 | M | ⚠️ (test-only tool) |
| ✅ Pin `cloudflared` image to a digest | Same supply-chain rule as Actions | P1 | S | ✅ |

### Download site

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ✅ `/latest.json` | Lets the app's update check ask the site, not GitHub | P2 | S | ✅ |
| ✅ Release feed (Atom) | Subscribe to new versions | P3 | S | ✅ |
| ✅ Screenshot or short clip of the real app | Show the real thing; keep it generic | P2 | S | ✅ |
| ✅ Changelog page | All release notes on one page | P3 | S | ✅ |
| ✅ Rate limiting on `/downloads` | Protect the GitHub API quota behind the tunnel | P2 | S | ✅ (stdlib token bucket in `httpserve/ratelimit.go`) |

### Distribution

| Item | Why | P | Effort | Deps |
| --- | --- | --- | --- | --- |
| ⏳ Homebrew cask (private tap) | `brew install --cask bikasend` | P3 | S | ✅ |
| ⏳ winget / Scoop manifest | One-line Windows install | P3 | S | ✅ |
| ⏳ Linux AppImage or `.deb` | Easier than a zip | P3 | M | ✅ |
| ⏳ Apple-silicon-only and Intel-only builds | Smaller downloads than universal | P3 | S | ✅ |

---

## 3. Out of scope

Bikasend stays a small, sharp tool. Say no to these unless the human asks for them:

- Pre-request or test scripts, injectable JavaScript, assertions, test suites.
- Collections you build by hand. The Swagger files are the collection.
- Cloud sync, accounts, teams, sharing through a server. Git is the sharing layer.
- Mock servers, load testing, monitors.
- Telemetry or analytics of any kind.
- Storing secret values on disk, in any form, including "encrypted".

---

## 4. Open questions

1. **AWS accounts and regions:** which profiles and regions will most teams use? This sets the
   defaults in `.bika.json` examples and docs.
2. **Secrets Manager only, or Parameter Store too?** The design supports both; SSM is planned for
   P2.
3. ~~**File name**~~ **Decided (9 Nov 2026):** `.bika.json` in the repo root, replacing
   `.bika.json`. No migration, since no one uses the old name in production yet.
4. **Prompted values across restarts:** keep them memory-only (current design), or offer the OS
   keychain? A keychain needs `security` (macOS), `secret-tool` (Linux) and the Credential Manager
   (Windows): stdlib via `exec`, but three code paths.
5. **Production guard:** should environments named `prod*` always ask "Send to production?"
   before each request?
6. **Who owns `.bika.json` in each service repo:** the service team, or the platform team?
