# Error Handling Rules

## Sentinels

Define sentinels in the contract, prefixed with the service name:

```go
// pkg/sdk/bikasend/errors.go
var ErrInvalidRequest = errors.New("bikasend: invalid request")
```

## Wrapping

- `errors.Join(sentinel, err)`, sentinel first. Callers classify with `errors.Is`.
- Never discard the underlying error.
- An error that leaves `core` carries a sentinel of its own service.

## Desktop app

- A bound method returns `error`; Wails rejects the JS Promise with its message, and the UI shows it.
- A non-2xx HTTP response is not an error. `Send` returns it as a normal response.
- One bad swagger file must not fail the whole scan. Record its error on that spec and go on.

## Site

- Handlers answer with `http.Error` or `http.NotFound` and a generic message. Log the detail with
  `slog`; never send internal paths or errors to the client.
- No `panic` outside `main` and route registration. `pkg/httpserve` recovers handler panics.
