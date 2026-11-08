// Package refuse is the one error for input the engine will not take: a program,
// a rule, an event or a staff act that is wrong as the caller wrote it. The HTTP
// layer answers it with 400; everything else is the engine's own fault.
package refuse

import (
	"errors"
	"fmt"
)

// Error is a refusal, with the reason a caller can act on.
type Error struct{ Msg string }

func (e Error) Error() string { return e.Msg }

// New refuses with a formatted reason.
func New(format string, a ...any) error { return Error{fmt.Sprintf(format, a...)} }

// Is reports whether err is, or wraps, a refusal.
func Is(err error) bool {
	var e Error
	return errors.As(err, &e)
}
