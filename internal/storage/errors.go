package storage

import "errors"

var (
	// ErrNotFound reports a missing row.
	ErrNotFound = errors.New("storage: not found")

	// ErrStateConflict reports that a guarded transition found the operation
	// in a different state than expected. Under concurrency this is an
	// ordinary outcome: another actor got there first.
	ErrStateConflict = errors.New("storage: operation is no longer in the expected state")

	// ErrIdempotencyConflict reports an idempotency key reused with a
	// different request body. Returning the first operation would execute
	// something the caller did not ask for.
	ErrIdempotencyConflict = errors.New("storage: idempotency key reused with a different payload")
)
