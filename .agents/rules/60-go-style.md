# Go Style Rules

## Naming

- No stuttering: `bikasend.Service`, not `bikasend.BikasendService`.
- Initialisms: `ID`, `URL`, `JSON`, `HTTP` — not `Id`, `Url`, `Json`.
- Methods bound to the frontend are verbs: `Scan`, `Send`, `PickFolder`.

## Code Style

- Early returns; golden path stays least nested.
- Prefer `var x T` over `x := T{}` for zero-value declarations.
- Group imports as stdlib / external / internal.
- Prefer Go 1.27 `new(expr)` for a pointer to a value — do not add `ptr` helpers.
- `gofmt` and `go vet ./...` clean.

## Data Passing

- Structs passed by value; return pointers only when nil carries meaning.
- Return `[]T{}` (empty slice) not `nil` for lists the frontend reads, so JS gets `[]`, not `null`.
- Types crossing to JS live in `pkg/sdk/bikasend/types.go` with camelCase `json` tags.
- `context.Context` is the first parameter where work can block (HTTP).
- Inject dependencies through constructors (`core.NewService(client)`), as in `cmd/*/main.go`.

## Logging

- `log/slog` only, JSON handler in `cmd/site`. The desktop app does not log.
- Never log secrets, headers or bodies (`10-security.md`).
