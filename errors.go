package gomailer

import (
	"errors"
	"fmt"
)

// Sentinel errors classify failures. Use errors.Is to match a category and
// errors.As to reach a *TransportError for its debug transcript.
var (
	// ErrInvalidArgument indicates a caller-supplied value was invalid.
	ErrInvalidArgument = errors.New("gomailer: invalid argument")
	// ErrLogic indicates a programming error, such as an invalid state.
	ErrLogic = errors.New("gomailer: logic error")
	// ErrRuntime indicates a failure that arose during execution.
	ErrRuntime = errors.New("gomailer: runtime error")
	// ErrTransport classifies transport failures.
	ErrTransport = errors.New("gomailer: transport error")
	// ErrIncompleteDSN indicates a DSN is missing required components.
	ErrIncompleteDSN = errors.New("gomailer: incomplete DSN")
	// ErrUnsupportedScheme indicates a DSN scheme has no registered transport.
	ErrUnsupportedScheme = errors.New("gomailer: unsupported DSN scheme")
)

// TransportError is a transport-level error carrying an optional wrapped cause,
// an SMTP-style response code and an appendable debug transcript. It satisfies
// errors.Is(err, ErrTransport) and unwraps to its cause for errors.As/Is.
type TransportError struct {
	Msg   string // human-readable message
	Code  int    // SMTP response code, 0 if not applicable
	Cause error  // wrapped underlying error, if any
	debug string // appended debug transcript
}

// NewTransportError builds a TransportError with the given message.
func NewTransportError(msg string) *TransportError {
	return &TransportError{Msg: msg}
}

// Error implements error.
func (e *TransportError) Error() string {
	switch {
	case e.Code != 0 && e.Cause != nil:
		return fmt.Sprintf("gomailer: transport error (code %d): %s: %v", e.Code, e.Msg, e.Cause)
	case e.Code != 0:
		return fmt.Sprintf("gomailer: transport error (code %d): %s", e.Code, e.Msg)
	case e.Cause != nil:
		return fmt.Sprintf("gomailer: transport error: %s: %v", e.Msg, e.Cause)
	default:
		return "gomailer: transport error: " + e.Msg
	}
}

// Unwrap returns the wrapped cause for errors.Is/As.
func (e *TransportError) Unwrap() error {
	return e.Cause
}

// Is reports membership of ErrTransport (and any wrapped sentinel via Unwrap),
// making errors.Is(err, ErrTransport) true for all transport errors.
func (e *TransportError) Is(target error) bool {
	if target == ErrTransport {
		return true
	}
	return e.Cause != nil && errors.Is(e.Cause, target)
}

// Debug returns the accumulated debug transcript.
func (e *TransportError) Debug() string {
	return e.debug
}

// AppendDebug appends to the debug transcript (used by SMTP/RoundRobin).
func (e *TransportError) AppendDebug(s string) {
	e.debug += s
}
