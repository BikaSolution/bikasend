package site

import "errors"

var (
	ErrNotFound    = errors.New("site: not found")
	ErrUnavailable = errors.New("site: releases unavailable")
)
