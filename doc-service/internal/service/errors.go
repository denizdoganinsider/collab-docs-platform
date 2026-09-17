package service

import "errors"

// Sentinel errors the controllers map to HTTP statuses. Anything else that
// comes out of a service is a 500.
var (
	// ErrForbidden covers both "not a member" and "member without the right
	// role". A non-member gets 403, not 404: ids are sequential, so hiding
	// existence would be theatre.
	ErrForbidden = errors.New("forbidden")
	ErrNotFound  = errors.New("not found")
)

// ValidationError is a client mistake in the request body (400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }
