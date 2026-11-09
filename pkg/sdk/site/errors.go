package site

import "errors"

var (
	ErrNotFound      = errors.New("site: not found")
	ErrUnavailable   = errors.New("site: releases unavailable")
	ErrInvalidConfig = errors.New("site: invalid config")
)
