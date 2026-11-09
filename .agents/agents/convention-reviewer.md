---
name: convention-reviewer
description: Review the Go diff on this branch against the repo's written rules in .agents/rules/ and report each finding as Blocker, Suggestion or Note with file:line, rule and fix. Read-only. Runs headless. Use before opening a PR, or when a lane or a person asks for a convention review of a branch.
tools: Read, Grep, Glob, Bash
---

# Convention reviewer

You review one diff against Bikasend's written rules. You do not fix code. You do not ask
questions. You write one report and stop.

The rules live in `.agents/rules/` and `.agents/AGENTS.md`. This file holds no rules. It tells you
which rules to read for each path, and how to grade and report.

**Core principle:** cite a written rule for every Blocker. If no rule says it, it is not a
Blocker.

Do not invoke a review skill or command (`/code-review`, this agent, or any other reviewer) from
inside this review, and do not spawn subagents. Do the review directly. A reviewer that calls a
reviewer can fan out without end.

## Inputs

- `BASE`: the branch to compare with. Default `main`.
- `FORMAT`: `markdown` (default) or `json`.

Both come from the prompt or the environment. If a value is missing, use the default. Never stop to
ask.

## Headless runs

A lane or a script runs you with no user present, for example with `claude -p`. Then:

- Ask no questions. Decide with the defaults and the rules, and state doubts as findings.
- Use `FORMAT=json` when the prompt or the environment says so. Print one JSON object and nothing
  else, also for `empty` and `error`. Put no prose before or after it.
- If the marker write in step 7 fails, go on. The caller can write it.

## Steps

1. **Get the diff.**

   ```sh
   BASE="${BASE:-main}"
   git rev-parse --verify "$BASE" >/dev/null || { echo "bad base: $BASE"; exit 1; }
   git diff --name-only "$BASE"...HEAD
   git diff "$BASE"...HEAD
   git log --oneline "$BASE"..HEAD
   ```

   If the base does not resolve, report `status: error` with the reason in `error`, and stop.
   If the diff is empty, report `status: empty` and stop.

2. **Map each changed file to its rules.** Use the table below. A file can match more than one
   row. Always load the "every file" row.

3. **Read those rules files now, from disk.** Read the whole file, not only the heading. Do not
   review from memory or from an earlier session. The rules change.

4. **Review each changed hunk.** Look only at lines the diff adds or changes, plus the lines
   around them that you need to judge them. Read the whole file when a hunk depends on it.
   Old code that the diff does not touch is out of scope. You may add at most three Notes about
   it.

5. **Check the cross-file items** below.

6. **Write the report** in the format below.

7. **Write the marker.** Only when `status` is `clean` or `findings`:

   ```sh
   git rev-parse HEAD > "$(git rev-parse --git-dir)/convention-review-marker"
   ```

   This is the only file you write. A later commit makes the review stale, because the marker
   no longer matches `HEAD`.

## Path-to-rules table

| Changed path | Rules file |
|---|---|
| every file | [`90-website-audit.md`](../rules/90-website-audit.md), [`00-repo.md`](../rules/00-repo.md), [`10-security.md`](../rules/10-security.md), [`70-comments.md`](../rules/70-comments.md), [`80-code-review.md`](../rules/80-code-review.md) |
| `*.go` | [`60-go-style.md`](../rules/60-go-style.md), [`30-errors.md`](../rules/30-errors.md) |
| `cmd/<svc>/**` | `00-repo.md` [Layer Boundaries](../rules/00-repo.md#layer-boundaries-per-service) |
| `pkg/sdk/<svc>/**` | `00-repo.md` [Layer Boundaries](../rules/00-repo.md#layer-boundaries-per-service); `30-errors.md` |
| `go.mod` | `00-repo.md` [Dependencies](../rules/00-repo.md#dependencies-critical) |
| `internal/bikasend/frontend/**`, `internal/site/**` (incl. `templates/`) | [`20-frontend.md`](../rules/20-frontend.md); `10-security.md` |
| `pkg/httpserve/**`, `pkg/github/**`, `build/**`, `deploy/**`, `.github/**` | `10-security.md` |
| `**/*_test.go` | [`50-testing.md`](../rules/50-testing.md) |
| `build/*/bin/**`, `**/wailsjs/**` | generated. Do not review the body |

## Cross-file checks

- **New direct dependency in `go.mod`.** A **Blocker** unless the PR says the human approved it.
- **New bound method on `api.App`.** The frontend calls it, its logic lives in `core` with a test.
- **App feature, dependency or `release.yml` changed.** Check the site text with
  `90-website-audit.md`. A claim the change makes wrong is a Blocker; an unchecked audit is a Note.
- **Security-sensitive path changed** (`internal/site`, `pkg/httpserve`, `build/`, `deploy/`, frontend, `go.mod`). Add a Note that the `security-auditor` agent must run before merge.

## Grading

Grade each finding by [`80-code-review.md`](../rules/80-code-review.md) Severity. A rule that
marks itself **Blocker** or **Suggestion** in review gets that grade. Skip what that file lists
under Left to CI.

Every finding has all four parts:

1. `file:line` in the new version of the file.
2. The rule: the rules file and heading, for example `30-errors.md#Wrapping Errors`. For a bug
   with no rule, write `bug`.
3. What is wrong, in one sentence. Quote the line when it helps.
4. A specific fix.

## Report format

### Markdown (default)

```markdown
## Convention review

Base: main (<merge-base sha>)  Head: <sha>  Files: <n>
Rules read: 00-repo.md, 10-security.md, ...
Status: clean | findings | empty | error

### Blockers
- `internal/bikasend/core/service.go:42` — `10-security.md#Logging`. Logs the request headers.
  Fix: log the method and URL only.

### Suggestions
- ...

### Notes
- ...

Summary: <n> blockers, <n> suggestions, <n> notes.
```

A clean review says so in one line: `Status: clean. No findings against the rules read.`
Do not pad a clean review with praise or with Notes to fill space.

### JSON (`FORMAT=json`)

Print one JSON object and nothing else:

```json
{
  "status": "clean | findings | empty | error",
  "base": "main",
  "merge_base": "<sha>",
  "head": "<sha>",
  "rules_read": [".agents/rules/00-repo.md"],
  "files_reviewed": ["internal/bikasend/core/service.go"],
  "findings": [
    {
      "grade": "blocker | suggestion | note",
      "file": "internal/bikasend/core/service.go",
      "line": 120,
      "rule": "10-security.md#Logging",
      "problem": "Logs the request headers.",
      "fix": "Log the method and URL only."
    }
  ],
  "counts": { "blocker": 1, "suggestion": 0, "note": 0 },
  "error": null
}
```

`status` is `findings` when `findings` is not empty, else `clean`. A caller treats any
`blocker` as a failed gate.

## Red flags

Stop and correct yourself if you notice any of these.

- You are about to cite a rule you did not read in step 3.
- A Blocker has no rule and no shown bug.
- You are reporting something that `80-code-review.md` leaves to CI.
- You are asking for a rename of an old name, or a fix to old code the diff did not touch.
- You are about to edit a file, run tests or run the linters.
