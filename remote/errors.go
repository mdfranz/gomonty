package remote

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	monty "github.com/mdfranz/gomonty"
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
	// Frames is the traceback, outermost call first. It is the same frame type
	// as monty.RuntimeError.Frames, so code that reads local tracebacks works
	// unchanged.
	Frames []monty.Frame
	// Unicode and JSON carry the structured fields of UnicodeDecodeError,
	// UnicodeEncodeError and json.JSONDecodeError. At most one is set, and
	// neither is for most exceptions.
	Unicode *UnicodeErrorData
	JSON    *JSONErrorData

	traceback []*montyv1.StackFrame
}

// UnicodeErrorData is the constructor arguments of a UnicodeDecodeError or
// UnicodeEncodeError.
type UnicodeErrorData struct {
	Encoding string
	// Object is the input that failed: Bytes for a decode error, String for an
	// encode error.
	Object monty.Value
	// Start and End are byte offsets for a decode error and character indexes
	// for an encode error. End is exclusive.
	Start, End uint64
	Reason     string
}

// JSONErrorData is the attributes of a json.JSONDecodeError.
type JSONErrorData struct {
	// Msg is the bare message, without the ": line N column M (char K)" suffix.
	Msg string
	// Doc is the document being parsed. It is nil when the worker left it out
	// because it was too large.
	Doc    *string
	Pos    uint64
	Lineno uint64
	Colno  uint64
}

func (e *PythonError) Error() string {
	if e.Message == "" {
		return e.Type
	}
	return e.Type + ": " + e.Message
}

// TracebackString renders the traceback the way monty.RuntimeError does:
// "Traceback (most recent call last):", one block per frame, then the
// exception. With no frames it is just the exception line.
func (e *PythonError) TracebackString() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	if len(e.traceback) > 0 {
		b.WriteString("Traceback (most recent call last):\n")
	}
	for _, f := range e.traceback {
		fmt.Fprintf(&b, "  File \"%s\", line %d", f.GetFilename(), f.GetStart().GetLine())
		if !f.GetHideFrameName() {
			name := "<module>"
			if f.FrameName != nil {
				name = f.GetFrameName()
			}
			b.WriteString(", in " + name)
		}
		b.WriteString("\n")
		if f.PreviewLine == nil {
			continue
		}
		line := f.GetPreviewLine()
		trimmed := strings.TrimLeft(line, " \t")
		b.WriteString("    " + trimmed + "\n")
		start, end := f.GetStart(), f.GetEnd()
		if f.GetHideCaret() || start.GetLine() != end.GetLine() || end.GetColumn() <= start.GetColumn() {
			continue
		}
		// Columns are 1-based characters of the full line; the preview drops
		// its indentation.
		indent := utf8.RuneCountInString(line) - utf8.RuneCountInString(trimmed)
		pad := int(start.GetColumn()) - 1 - indent
		if pad < 0 {
			continue
		}
		b.WriteString("    " + strings.Repeat(" ", pad) + strings.Repeat("~", int(end.GetColumn()-start.GetColumn())) + "\n")
	}
	b.WriteString(e.Error())
	return b.String()
}

func pythonError(e *montyv1.RaisedException) *PythonError {
	err := &PythonError{Type: e.GetExcType(), Message: e.GetMessage(), traceback: e.GetTraceback()}
	for _, f := range e.GetTraceback() {
		frame := monty.Frame{
			Filename:  f.GetFilename(),
			Line:      f.GetStart().GetLine(),
			Column:    f.GetStart().GetColumn(),
			EndLine:   f.GetEnd().GetLine(),
			EndColumn: f.GetEnd().GetColumn(),
		}
		if f.FrameName != nil {
			frame.FunctionName = f.FrameName
		}
		if f.PreviewLine != nil {
			frame.SourceLine = f.PreviewLine
		}
		err.Frames = append(err.Frames, frame)
	}
	switch d := e.GetData().GetKind().(type) {
	case *montyv1.ExcData_Unicode:
		u := d.Unicode
		data := &UnicodeErrorData{Encoding: u.GetEncoding(), Start: u.GetStart(), End: u.GetEnd(), Reason: u.GetReason()}
		switch o := u.GetObject().(type) {
		case *montyv1.UnicodeErrorData_ObjectBytes:
			data.Object = monty.Bytes(append([]byte(nil), o.ObjectBytes...))
		case *montyv1.UnicodeErrorData_ObjectStr:
			data.Object = monty.String(o.ObjectStr)
		}
		err.Unicode = data
	case *montyv1.ExcData_Json:
		j := d.Json
		err.JSON = &JSONErrorData{Msg: j.GetMsg(), Doc: j.Doc, Pos: j.GetPos(), Lineno: j.GetLineno(), Colno: j.GetColno()}
	}
	return err
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
