package admin

import "errors"

// errorsAs wraps errors.As so handlers need not import errors for one call.
func errorsAs(err error, target any) bool { return errors.As(err, target) }
