// Package feed fetches and parses feeds.
//
// Fetching follows redirects itself, so that it can report when an address has moved
// permanently. It sends ETag/Last-Modified so that an unchanged feed returns 304, has an overall
// timeout and a size limit, and decodes the character encoding from the BOM, Content-Type or the
// XML declaration. Parsing turns RSS, Atom, RDF and JSON Feed into the same shape.
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

// Validators are the ETag and Last-Modified from the previous fetch.
type Validators struct {
	ETag         string
	LastModified string
}

// Redirect is where the feed was redirected to.
type Redirect struct {
	// To is the final address after all redirects.
	To string
	// Permanent is true if every step was a 301 or 308.
	Permanent bool
}

// Result is a fetch. NotModified means 304: everything else is then empty except Redirect.
type Result struct {
	NotModified  bool
	Text         string
	URL          string
	ETag         string
	LastModified string
	Redirect     *Redirect
}

// HTTPError is a response that is neither 2xx nor 304.
type HTTPError struct {
	Status     int
	StatusText string
	// RetryAfter is the number of seconds from Retry-After, -1 if the server didn't send it.
	RetryAfter int64
}

func (e *HTTPError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("HTTP %d %s", e.Status, e.StatusText))
}

// Name is the error's name in the log, as in the TypeScript version.
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

// Fetch fetches the feed. client is normally http.DefaultClient; it must not follow redirects
// itself (that is done here), so CheckRedirect is overridden.
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
				return nil, netx.Errorf("More than %d redirects", maxRedirects)
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
	tooBig := netx.Errorf("The feed is larger than %d MB", MaxBytes/1024/1024)
	if n, err := strconv.ParseFloat(res.Header.Get("Content-Length"), 64); err == nil && n > MaxBytes {
		return nil, tooBig
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	if err != nil {
		return nil, netx.FetchError(err)
	}
	if len(body) > MaxBytes {
		return nil, tooBig // stopping reading aborts the download
	}
	return body, nil
}

// IsHTTPError returns the HTTP error behind err, if there is one.
func IsHTTPError(err error) (*HTTPError, bool) {
	var h *HTTPError
	ok := errors.As(err, &h)
	return h, ok
}
