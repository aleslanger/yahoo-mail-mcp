package mail

import "errors"

// temporaryError marks a failure that may succeed on a later attempt, such as
// a refused connection or Yahoo briefly rejecting rapid repeated logins.
// Callers must only retry operations that are safe to repeat.
type temporaryError struct{ msg string }

func (e temporaryError) Error() string { return e.msg }

func (temporaryError) Temporary() bool { return true }

// NewTemporaryError returns an error that IsTemporary reports as retryable.
func NewTemporaryError(msg string) error { return temporaryError{msg: msg} }

// IsTemporary reports whether err, or an error it wraps, is retryable.
func IsTemporary(err error) bool {
	var t interface{ Temporary() bool }
	return errors.As(err, &t) && t.Temporary()
}
