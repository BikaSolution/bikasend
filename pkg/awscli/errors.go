package awscli

import "errors"

var (
	ErrNotInstalled = errors.New("awscli: aws CLI not found; install AWS CLI v2 or set its path in Settings")
	ErrNotSignedIn  = errors.New("awscli: not signed in")
	ErrProfile      = errors.New("awscli: unknown profile")
	ErrAccessDenied = errors.New("awscli: access denied")
	ErrNotFound     = errors.New("awscli: not found")
	ErrInvalidInput = errors.New("awscli: invalid input")
	ErrTimeout      = errors.New("awscli: timed out")
	ErrOutput       = errors.New("awscli: unexpected output")
	ErrCLI          = errors.New("awscli: command failed")
)
