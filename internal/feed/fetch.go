// Package feed hämtar och tolkar flöden.
//
// Hämtningen följer omdirigeringar själv, så att den kan säga till när en adress flyttat
// permanent. Den skickar ETag/Last-Modified så att ett oförändrat flöde ger 304, har en total
// timeout och en storleksgräns, och avkodar teckenkodningen ur BOM, Content-Type eller
// XML-deklarationen. Tolkningen gör RSS, Atom, RDF och JSON Feed till samma form.
package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	mastorss "github.com/maxbengtzen/masto-rss"
	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/netx"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

const (
	Timeout      = 30 * time.Second
	MaxBytes     = 5 * 1024 * 1024
	maxRedirects = 5
)

// Validators är ETag och Last-Modified från förra hämtningen.
type Validators struct {
	ETag         string
	LastModified string
}

// Redirect är vart flödet omdirigerades.
type Redirect struct {
	// To är slutadressen efter alla omdirigeringar.
	To string
	// Permanent är sant om varje steg var 301 eller 308.
	Permanent bool
}

// Result är en hämtning. NotModified betyder 304: då är resten tomt utom Redirect.
type Result struct {
	NotModified  bool
	Text         string
	URL          string
	ETag         string
	LastModified string
	Redirect     *Redirect
}

// HTTPError är ett svar som inte är 2xx eller 304.
type HTTPError struct {
	Status     int
	StatusText string
	// RetryAfter är sekunder enligt Retry-After, -1 om servern inte skickade det.
	RetryAfter int64
}

func (e *HTTPError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("HTTP %d %s", e.Status, e.StatusText))
}

// Name är felets namn i loggen, som i TypeScript-versionen.
func (e *HTTPError) Name() string { return "HttpError" }

func retryAfterSeconds(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
		return n
	}
	ms, ok := js.ParseDate(value)
	if !ok {
		return -1
	}
	secs := (ms - time.Now().UnixMilli()) / 1000
	if d := (ms - time.Now().UnixMilli()) % 1000; d >= 500 {
		secs++
	}
	return max(0, secs)
}

// Fetch hämtar flödet. client är normalt http.DefaultClient; den får inte följa omdirigeringar
// själv (det görs här), så CheckRedirect sätts om.
func Fetch(ctx context.Context, client *http.Client, url string, v Validators) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	current := url
	permanent := true
	for hops := 0; ; hops++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current, nil)
		if err != nil {
			return nil, netx.FetchError(err)
		}
		req.Header.Set("User-Agent", mastorss.UserAgent())
		req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/feed+json, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.8")
		if v.ETag != "" {
			req.Header.Set("If-None-Match", v.ETag)
		}
		if v.LastModified != "" {
			req.Header.Set("If-Modified-Since", v.LastModified)
		}
		res, err := c.Do(req)
		if err != nil {
			return nil, netx.FetchError(err)
		}
		location := res.Header.Get("Location")
		if res.StatusCode >= 300 && res.StatusCode < 400 && res.StatusCode != 304 && location != "" {
			res.Body.Close()
			if hops >= maxRedirects {
				return nil, netx.Errorf("Fler än %d omdirigeringar", maxRedirects)
			}
			if res.StatusCode != 301 && res.StatusCode != 308 {
				permanent = false
			}
			next, err := weburl.ParseRef(location, current)
			if err != nil {
				return nil, netx.Named("TypeError", "Invalid URL")
			}
			current = next.Href()
			continue
		}

		var redirect *Redirect
		if hops > 0 {
			redirect = &Redirect{To: current, Permanent: permanent}
		}
		if res.StatusCode == 304 {
			res.Body.Close()
			return &Result{NotModified: true, Redirect: redirect}, nil
		}
		if res.StatusCode < 200 || res.StatusCode > 299 {
			res.Body.Close()
			return nil, &HTTPError{Status: res.StatusCode, StatusText: netx.StatusText(res), RetryAfter: retryAfterSeconds(res.Header.Get("Retry-After"))}
		}
		body, err := readLimited(res)
		if err != nil {
			return nil, err
		}
		return &Result{
			Text:         DecodeBody(body, res.Header.Get("Content-Type")),
			URL:          current,
			ETag:         res.Header.Get("ETag"),
			LastModified: res.Header.Get("Last-Modified"),
			Redirect:     redirect,
		}, nil
	}
}

func readLimited(res *http.Response) ([]byte, error) {
	defer res.Body.Close()
	tooBig := netx.Errorf("Flödet är större än %d MB", MaxBytes/1024/1024)
	if n, err := strconv.ParseFloat(res.Header.Get("Content-Length"), 64); err == nil && n > MaxBytes {
		return nil, tooBig
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	if err != nil {
		return nil, netx.FetchError(err)
	}
	if len(body) > MaxBytes {
		return nil, tooBig // att sluta läsa avbryter nedladdningen
	}
	return body, nil
}

// IsHTTPError ger HTTP-felet bakom err, om det finns ett.
func IsHTTPError(err error) (*HTTPError, bool) {
	var h *HTTPError
	ok := errors.As(err, &h)
	return h, ok
}
