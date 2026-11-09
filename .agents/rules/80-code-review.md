# Code Review Rules

How a review grades a finding, and what it leaves to tools. The rules it checks live in the other
files in this folder. The `convention-reviewer` agent (`.agents/agents/convention-reviewer.md`)
reads this file and those rules.

## Severity

- **Blocker**: a new or changed line breaks a mandatory written rule, or a rule that marks itself
  **Blocker** in review. Also any bug you can show: a nil dereference, a lost error, a broken
  platform build.
- **Suggestion**: the line breaks a rule that says "prefer" or "should", or a rule that marks
  itself **Suggestion** in review.
- **Note**: something the author should know. It needs no change.

Cite a written rule for every Blocker. If no rule says it and no bug shows it, it is not a
Blocker. If you are not sure a finding is real, grade it one step lower and say why.

## Left to tools

A review does not report these:

- Formatting and import order (`gofmt`).
- `go vet ./...` findings.
- Whether tests pass.
