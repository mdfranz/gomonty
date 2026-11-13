package remote

import (
	"errors"
	"fmt"

	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// ErrUnsupported reports an operation the remote backend does not support yet.
var ErrUnsupported = errors.New("remote: not supported by the remote backend yet")

// PythonError is a Python exception raised by code running on the worker.
type PythonError struct {
	// Type is the exception class name, such as "ValueError".
	Type string
	// Message is the exception message, empty if it had none.
	Message string
}

func (e *PythonError) Error() string {
	if e.Message == "" {
		return e.Type
	}
	return e.Type + ": " + e.Message
}

func pythonError(e *montyv1.RaisedException) *PythonError {
	return &PythonError{Type: e.GetExcType(), Message: e.GetMessage()}
}

// TypingError reports that the worker's type checker rejected a snippet. The
// snippet did not run.
type TypingError struct {
	// Diagnostics are rendered in the session's type-check format.
	Diagnostics string
}

func (e *TypingError) Error() string { return "type check failed:\n" + e.Diagnostics }

// SessionRefusedError reports that the worker refused to open a session.
type SessionRefusedError struct{ Reason string }

func (e *SessionRefusedError) Error() string {
	return fmt.Sprintf("remote: the worker refused the session: %s", e.Reason)
}

// ConnectionLostError reports that the stream to the worker failed or ended
// before the worker finished a turn: the worker crashed, was restarted, hung
// up, or the network dropped. The session is closed and cannot be reused.
// Err is the underlying cause, such as a gRPC status. Use errors.As to detect
// it.
type ConnectionLostError struct {
	// Op is "send" or "receive".
	Op  string
	Err error
}

func (e *ConnectionLostError) Error() string {
	return fmt.Sprintf("remote: connection lost during %s: %v", e.Op, e.Err)
}

func (e *ConnectionLostError) Unwrap() error { return e.Err }
