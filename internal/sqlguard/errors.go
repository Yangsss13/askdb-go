// Package sqlguard validates and normalizes SQL produced upstream (e.g. by an
// LLM) before it is executed against the read-only demo database. It parses the
// SQL into an AST and enforces a strict allowlist: only single read-only SELECT
// statements over a fixed set of tables, with a bounded outermost LIMIT.
//
// The guard is defense-in-depth. It does not replace the least-privilege
// database account (askdb_reader) that is the ultimate security boundary.
package sqlguard

import (
	"errors"
	"fmt"
)

// ErrRejected is returned when SQL is deterministically rejected by a guard
// rule (disallowed statement, table, function, LIMIT, etc.). It is a normal
// business outcome: the caller persists the job as failed and ACKs the message.
//
// The concrete rejection reason is wrapped for internal logging via %w, but the
// client-facing message must remain a stable, safe string and never expose the
// parser or database internals.
var ErrRejected = errors.New("sqlguard: rejected")

// GuardError is a deterministic rejection with an internal reason. It wraps
// ErrRejected so callers can match it with errors.Is(err, ErrRejected). The
// reason is for internal logging only and must never be shown to clients.
type GuardError struct {
	Reason string
}

func (e *GuardError) Error() string { return "sqlguard: rejected: " + e.Reason }

// Unwrap lets errors.Is(err, ErrRejected) succeed for any GuardError.
func (e *GuardError) Unwrap() error { return ErrRejected }

// reject builds a GuardError with the given internal reason. It returns the
// error interface (not *GuardError) so that a nil is a genuine nil interface
// and callers can safely compare the result against nil.
func reject(reason string) error { return &GuardError{Reason: reason} }

// rejectf builds a GuardError with a formatted internal reason.
func rejectf(format string, args ...any) error {
	return &GuardError{Reason: fmt.Sprintf(format, args...)}
}
