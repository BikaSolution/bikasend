---
name: create-pr
description: Create a pull request using the project's PR template. Reads .github/pull_request_template.md dynamically and fills in sections from branch context.
user-invocable: true
---

# Create Pull Request

A PR is only as good as its description. The project has a PR template for a reason — read it,
fill it properly, and verify the code is clean before publishing.

**Core principle:** The PR template is the source of truth for structure. Never hardcode
sections — always read the template fresh.

## Arguments

- `base`: Target branch (default: `main`). For stacked PRs, pass the parent feature branch.
- `title`: PR title override (auto-generated from branch/commits if omitted).
- `draft`: Create as draft PR (default: false).

## Pipeline

### Step 1: Read the PR Template

Read `.github/pull_request_template.md` from the repo root. Parse:

- Section headings (`#` lines)
- Checklists (`- [ ]` lines) — preserve as unchecked for the author
- HTML comments — hints for what to write in each section

<hard-gate>
NEVER hardcode PR sections. The template is the contract with the team. If it changes, your
output must change with it. Read it every time.
</hard-gate>

### Step 2: Gather Git Context

```sh
git log <base>..HEAD --oneline          # commits on this branch
git diff <base>...HEAD --stat           # files changed summary
git diff <base>...HEAD                  # full diff for analysis
git branch --show-current               # current branch name
```

Replace `<base>` with the `base` argument (default: `main`). Run these in parallel — they are
independent.

### Step 3: Find the Issue

Parse the branch name for a GitHub issue number. Pattern: a leading number, `^\d+-`.

Example: `123-some-description` → `#123`

If the branch has no number, check the commit messages for `#123` before giving up.

The number feeds the body's issue section (Step 5), not the title.

### Step 4: Run the Quality Gates

```sh
go vet ./...
go test ./...
wails build
```

Never open a PR on a red build — CI will fail anyway. Fix the failures, or stop and tell the
user what broke.

### Step 5: Generate Title and Body

**Title** (if not provided via `title` argument) — a conventional commit, because the repo
squash-merges and the PR title becomes the commit message:

```text
<type>(<scope>): <subject>
```

```sh
feat(swagger): read OpenAPI 3 servers
fix(ui): keep the selected operation after a rescan
docs: add pull request rules for agents
```

- `type` — `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `perf`, `build`, `ci`.
- `scope` — the area the diff touches (`app`, `ui`, `swagger`, `site`, `deps`, `agents`). Omit for repo-wide changes. Derive it from
  `git diff --stat`, not from the branch name.
- `subject` — imperative, lowercase, no trailing period.
- No issue number in the title. It goes in the body instead — see the issue section below.
- Max 70 characters. Breaking changes take `!` before the colon: `feat(ui)!: ...`.

**Body** — fill each template section by analyzing the diff and commits:

| Section type | How to fill |
|-------------|-------------|
| Descriptive sections | Replace HTML comment hints with content inferred from changes |
| Checklists | Preserve as-is with unchecked boxes — author fills these |
| Issue section | `Refs #123` links the issue; `Fixes #123` also closes it on merge. Write `N/A` when there is no issue |
| Review guidelines | Keep the template text as-is; add what a reviewer needs to test the change locally |
| Showcase/visual sections | If presentation files changed, note screenshots/GIFs needed. If no UI changes, write `N/A — no UI changes` |

For the current template, the issue section fills in like this:

```markdown
## Linked issue

Refs #42
```

Use `Fixes #42` only when merging the PR should close the issue.

**Voice** — same budget as [`70-comments.md`](../../rules/70-comments.md). Write the way you'd
explain the change to a teammate:

- Plain words and short sentences. No big vocabulary, no multi-clause sentences, no marketing
  tone ("composed experience", "restrained animations", "honest, tracked").
- Say what changed and why. Skip the flourish.
- Keep the genuinely useful links and tables (design frames, issues, goldens) — the problem is
  the prose around them, not the references.
- If a sentence needs re-reading to parse, rewrite it shorter.

### Step 6: Confirm with User

Show the user:

- PR title
- Full PR body
- Base branch
- Whether it will be a draft

Ask for confirmation. Opening a PR is outward-facing and hard to undo.

### Step 7: Create the PR

**Never pass the body as a shell argument.** PR bodies hold backticks, `$`, quotes, and fenced
code blocks — all of which the shell will mangle or interpolate. Write the body to a file with
your file-write tool, then hand `gh` the path:

```sh
gh pr create \
  --base main \
  --assignee @me \
  --title '<title>' \
  --body-file <scratchpad>/pr-body.md
```

- Write `pr-body.md` with the file-write tool, not with `echo`, `printf`, or shell redirection.
  The point is to keep the body out of the shell entirely.
- Put it in the session scratchpad or `mktemp` output — never in the repo, where it can be
  committed by accident.
- Single-quote the title. If the subject contains an apostrophe, reword it — imperative
  lowercase subjects rarely need one.
- `--assignee @me` is not optional — every PR is assigned to the git user who opens it.
- Add `--draft` if draft mode was requested. Return the PR URL.

Delete the body file once the PR is created.

## Red Flags

Stop and ask the user if:

- PR template file not found — do not invent a format
- No commits on branch relative to base — nothing to PR
- Branch name has no issue number and no title provided — ask for a title
- Build, tests, or lint fail — fix first

## Stacked PRs

When this branch builds on another feature branch (not `main`), set `--base` to the parent
feature branch. This chains PRs so GitHub merges them in order.

## Anti-Patterns

- **Non-conventional titles** — the title lands in `main`'s history verbatim. `Fixed the thing`
  and `#42: Add search` both pollute the log.
- **Issue number in the title** — `feat(ui): add response search (#42)` wastes title
  characters on something the body's `Refs` line already does, properly.
- **Hardcoding template sections** — the template evolves. Read it fresh every time.
- **Forgetting `--assignee @me`** — unassigned PRs get lost.
- **Creating a PR with failing gates** — CI will fail anyway. Fix first.
- **Body inlined into `--body`** — a heredoc or a quoted string will eat backticks, expand `$`,
  or break on a line that happens to read `EOF`. Use `--body-file`.
- **Vague descriptions** — "Various improvements" tells reviewers nothing.
- **Word soup** — inflated vocabulary and complex sentences. Reviewers have called this out
  explicitly; it makes descriptions harder to read, not more thorough.
- **Forgetting the stacked PR base** — if this branch builds on another, pass `--base`.
