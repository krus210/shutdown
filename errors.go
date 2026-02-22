package shutdown

import "errors"

// joinErrors aggregates multiple errors into one using [errors.Join].
// Nil errors are filtered out. Returns nil if no non-nil errors remain.
func joinErrors(errs ...error) error {
	return errors.Join(errs...)
}
