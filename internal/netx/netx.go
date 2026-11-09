// Package netx beskriver fel som TypeScript-versionen gjorde i loggen: "Namn: meddelande
// (orsak)". Namnen (HttpError, MastodonError, TypeError och så vidare) står kvar så att loggen ser
// ut som förut.
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

// Error är ett fel med namn och, om det finns, orsaken bakom.
type Error struct {
	name, msg string
	cause     error
}

func (e *Error) Error() string { return e.msg }
func (e *Error) Name() string  { return e.name }
func (e *Error) Unwrap() error { return e.cause }

// CauseMessage är orsakens meddelande, som visas inom parentes.
func (e *Error) CauseMessage() string {
	if e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

// Named skapar ett fel med namn.
func Named(name, msg string) error { return &Error{name: name, msg: msg} }

// Errorf är `new Error(...)`.
func Errorf(format string, args ...any) error {
	return &Error{name: "Error", msg: fmt.Sprintf(format, args...)}
}

// FetchError gör ett nätverksfel till det fetch gav i Node: TimeoutError vid timeout, annars
// "TypeError: fetch failed" med orsaken. Orsaken är Go:s egen text.
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

// Describe är en kort beskrivning av ett fel för loggen, med orsaken om det finns en.
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

// StatusText är svarets statustext (res.statusText): det servern skrev efter koden.
func StatusText(res *http.Response) string {
	return strings.TrimSpace(strings.TrimPrefix(res.Status, strconv.Itoa(res.StatusCode)))
}
