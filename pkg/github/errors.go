package github

import "errors"

var (
	ErrInvalidRepo = errors.New("github: repo must be owner/name")
	ErrInvalidAPI  = errors.New("github: API URL must be https")
	ErrUpstream    = errors.New("github: request failed")
)
