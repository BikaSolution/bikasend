# Testing Rules

- Stdlib `testing` only. No testify, no gomock, no other test library.
- One test file per implementation file: tests for `swagger.go` go in `swagger_test.go`. Add cases to
  the existing table, not a new file.
- Tests and their test data live in `*_test.go` files only. No `testdata/` directories, no
  fixture/JSON/golden files. Declare swagger documents inline as string constants. For folder
  scans, write them into `t.TempDir()`.
- Table-driven tests for multi-scenario cases.
- Test `Send` against `httptest.NewServer`, site handlers with `httptest.NewRecorder` and
  `fstest.MapFS`. Never call a real network host.
- Do not test Wails itself or the window. Test `core`, which `api` calls.
- A port that wraps an external tool gets a small hand-written fake in the test file (see
  `fakeAWS` in `core/service_test.go`). No mock frameworks.
- A wrapper around an external CLI is tested with the helper-process pattern: the test binary
  plays the CLI (see `pkg/awscli/client_test.go`).
- The UI has one end-to-end test, `TestUI` in `frontend/embed_test.go`. It serves `dist/` with a
  stand-in for the Wails bridge that calls the real `core.Service`, and drives headless Chrome
  through a small stdlib DevTools client. No browser automation modules, no browser downloads. It
  skips without Chrome (`BIKASEND_CHROME` sets the path); CI's Ubuntu runner has one. Extend it
  when a UI flow changes.
- Parsers of untrusted input have a fuzz test with seeds inline (`FuzzParseJSON`, `FuzzParseYAML`).
  Run `make fuzz` after changing them; CI runs each for 30s. Never fuzz with default parallelism
  (one worker per CPU); use `make fuzz` or its `-parallel 2` and memory caps.
- Every limit has a test that crosses it: nesting depth, file size, folder total, work budgets.
  Build the hostile input in the test (`strings.Repeat`, a generator function), not in a file.
- Security behaviour gets a test: XSS-shaped names, path traversal, hidden files, size caps.

## No Duplicate Tests

- A case is a duplicate if it runs the same code path as an existing case and checks the same thing.
- An existing case misses a field? Add one assert to that case.
- Do not assert values the test set and never changed. They cannot fail.
