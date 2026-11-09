package bikasend

import "errors"

var (
	ErrNotSpec           = errors.New("bikasend: not a swagger or openapi document")
	ErrInvalidSpec       = errors.New("bikasend: invalid swagger document")
	ErrScan              = errors.New("bikasend: scan folder")
	ErrInvalidRequest    = errors.New("bikasend: invalid request")
	ErrRequestFailed     = errors.New("bikasend: request failed")
	ErrCancelled         = errors.New("bikasend: request cancelled")
	ErrUndefinedVariable = errors.New("bikasend: undefined variable")
	ErrVariableCycle     = errors.New("bikasend: variable cycle")
	ErrSecretHost        = errors.New("bikasend: secret not allowed for this host")
	ErrInsecureSecret    = errors.New("bikasend: secret over plain http")
	ErrSecretInFile      = errors.New("bikasend: secret value in a shared file")
	ErrInvalidVariable   = errors.New("bikasend: invalid variable")
	ErrInvalidConfig     = errors.New("bikasend: invalid .bika.json")
	ErrNoFolder          = errors.New("bikasend: no folder open")
	ErrUnknownEnv        = errors.New("bikasend: unknown environment")
	ErrSecrets           = errors.New("bikasend: load secrets")
	ErrAuth              = errors.New("bikasend: authentication")
	ErrCurl              = errors.New("bikasend: invalid curl command")
	ErrUpdate            = errors.New("bikasend: update check")
)
