// Package netx describes errors the way the TypeScript version did in the log: "Name: message
// (cause)". The names (HttpError, MastodonError, TypeError and so on) are kept so that the log looks
// the same as before.
package netx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Error is an error with a name and, if there is one, the cause behind it.
type Error struct {
	name, msg string
	cause     error
}

func (e *Error) Error() string { return e.msg }
func (e *Error) Name() string  { return e.name }
func (e *Error) Unwrap() error { return e.cause }

// CauseMessage is the cause's message, shown in parentheses.
func (e *Error) CauseMessage() string {
	if e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

// Named creates a named error.
func Named(name, msg string) error { return &Error{name: name, msg: msg} }

// Errorf is `new Error(...)`.
func Errorf(format string, args ...any) error {
	return &Error{name: "Error", msg: fmt.Sprintf(format, args...)}
}

// FetchError turns a network error into what fetch returned in Node: TimeoutError on timeout,
// otherwise "TypeError: fetch failed" with the cause. The cause is Go's own text.
func FetchError(err error) error {
	if err == nil {
		return nil
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return &Error{name: "TimeoutError", msg: "The operation was aborted due to timeout"}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return &Error{name: "TypeError", msg: "fetch failed", cause: err}
}

// Describe returns a short description of an error for the log, with the cause if there is one.
func Describe(err error) string {
	if err == nil {
		return ""
	}
	name := "Error"
	if n, ok := err.(interface{ Name() string }); ok {
		name = n.Name()
	}
	s := name + ": " + err.Error()
	if c, ok := err.(interface{ CauseMessage() string }); ok && c.CauseMessage() != "" {
		s += " (" + c.CauseMessage() + ")"
	}
	return s
}

// StatusText is the response's status text (res.statusText): what the server wrote after the code.
func StatusText(res *http.Response) string {
	return strings.TrimSpace(strings.TrimPrefix(res.Status, strconv.Itoa(res.StatusCode)))
}
