# Comment Rules

**Less is always more.** A comment is a cost. Write one only when the code cannot say it.

**The test:** could a developer work this out by reading the name and the code? Then no comment.
If the purpose of a function, type, or variable is clear from its name, it gets no comment —
exported or not. Most code you write should have zero comments. When in doubt, leave it out.

## Default: no comment

Do not comment:

- Struct fields whose name already says it (`CreatedAt`, `DeviceID`, `Total`).
- Methods that do what their name says.
- Any line that restates the code below it.
- Section banners, decorative rules, `// --- helpers ---`.
- Params, returns, and errors, one per line. This is not Javadoc.

```go
// DON'T
// BaseURL is the base URL that is used as the prefix for every
// operation path in this specification document.
BaseURL string `json:"baseURL"`

// DO
BaseURL string `json:"baseURL"`
```

## When to comment

Comment the **why**, never the **what**:

- A non-obvious constraint (upstream quirk, ordering requirement, race).
- A deliberate choice a reader would otherwise "fix".
- A workaround, with the reason it exists.

```go
// DO — the reader can't see this from the code
// swag omits schemes; default to http.

// DON'T — the code already says this
// Loop over the operations and add each one to the list.
```

## How to write it

- One line. Two if you must. Never five.
- Skip the jargon — "idempotent" is fine, "leverages a monotonic reconciliation barrier" is not.
- No hedging, no essays, no restating the ticket.

```go
// DON'T
// This function leverages a recursive descent strategy in order to facilitate
// the synthesis of a representative payload from the underlying schema graph
// whilst guarding against unbounded recursion in self-referential definitions.

// DO
// Stop at depth 8 — self-referencing schemas would recurse forever.
```

## Simplified Technical English (STE)

Write comments to eliminate ambiguity:

- **Active voice only.** Say *who* or *what* acts. Avoid `be`, `been`, `is done by`.
- **Imperative verbs for actions.** Use direct commands (*Accept*, *Retry*, *Lock*). Godoc is the exception — it keeps Go's third-person form: *Scan parses*, not *Parse*.
- **One term per concept.** Pick one word and keep it. Do not alternate between *check*, *verify*, and *validate*.
- **No phrasal verbs.** Write *Start*, not *Spin up*. Write *Remove*, not *Clean out*.
- **Max 3-word noun clusters.** Do not chain multi-word nouns.
- **Max 15 words per sentence.** Two sentences at most — see the budget above.

```go
// DON'T — passive, phrasal verb, ambiguous synonyms
// Verification of the payload is carried out here to ensure no bad data gets checked.

// DO — active, direct, precise
// Validate payload format before database write.
```

## Exported identifiers

Godoc only when the name does not already say it. Then it follows the same budget: one line, starts with the name.

```go
// DO
// Scan parses every swagger file under root; a bad file fails only its own spec.
func (a *App) Scan(root string) ([]Spec, error)

// DON'T
// Scan is a method on the App struct which is responsible for scanning the
// provided root directory. It takes a root path and returns a list of specs,
// and returns an error if the scan fails for any reason.
```

Unexported helpers usually need nothing.

## Also

- Delete commented-out code. Git remembers it.
- `TODO` needs an owner, not a ticket: `// TODO (name): ...`. The owner is your configured git user (`git config user.name`), verbatim.
- Update the comment when you change the code, or delete it. A stale comment is worse than none.
