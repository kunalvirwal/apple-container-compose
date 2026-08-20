package cli

import "errors"

type reportedError struct {
	err error
}

func (e *reportedError) Error() string {
	return e.err.Error()
}

func (e *reportedError) Unwrap() error {
	return e.err
}

func markErrorReported(err error) error {
	if err == nil {
		return nil
	}
	return &reportedError{err: err}
}

// IsErrorReported reports whether the CLI has already rendered a diagnostic
// for err. Callers should preserve the non-zero exit status without printing
// the error again.
func IsErrorReported(err error) bool {
	var reported *reportedError
	return errors.As(err, &reported)
}
