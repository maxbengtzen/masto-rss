// Package mastodon is the Mastodon API: instance info, token checks, posting and the account's
// latest posts.
//
// Every call has a timeout. Errors are classified: network errors, 5xx and 429 are transient,
// 401/403 mean the token is not accepted, and other 4xx mean Mastodon rejects the request for good.
// A Deprecation header in a response is passed on to OnDeprecation, so we notice when something we
// use is being phased out.
package mastodon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	mastorss "github.com/maxbengtzen/masto-rss"
	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/netx"
)

const timeout = 30 * time.Second

// Error is an error from Mastodon.
type Error struct {
	Status  int
	Message string
	// ResetAt is when the rate limit window opens again (ms), on 429. 0 = unknown.
	ResetAt int64
}

func (e *Error) Error() string { return e.Message }

// Name is the error's name in the log.
func (e *Error) Name() string { return "MastodonError" }

// Kind is "auth" (token not accepted), "rejected" (Mastodon says no for good) or "transient" (try
// again later).
func (e *Error) Kind() string {
	switch {
	case e.Status == 401 || e.Status == 403:
		return "auth"
	case e.Status >= 400 && e.Status < 500 && e.Status != 429 && e.Status != 408:
		return "rejected"
	}
	return "transient"
}

// Instance is what we need to know about the instance.
type Instance struct {
	Version string
	// APIVersion is api_versions.mastodon; 0 if the instance does not report it (older Mastodon or
	// another server).
	APIVersion       int
	MaxCharacters    int
	CharactersPerURL int
}

// App is the app behind a token.
type App struct {
	Name   string
	Scopes []string
}

// Account is the account behind a token.
type Account struct {
	ID        string
	Acct      string
	URL       string
	Followers int
	Posts     int
	// LastStatusAt is the date (YYYY-MM-DD) of the account's latest post, if any.
	LastStatusAt string
	Bot          bool
}

// StatusParams is what is sent with a post.
type StatusParams struct {
	Status     string `json:"status"`
	Visibility string `json:"visibility"`
	Language   string `json:"language,omitempty"`
	// QuotePolicy is only sent to instances with API version 7 or later.
	QuotePolicy string `json:"quote_approval_policy,omitempty"`
}

// Posted is a published post.
type Posted struct {
	ID  string
	URL string
}

// Recent is one of the account's latest posts.
type Recent struct {
	URL     string
	Content string
	Card    string
}

// Client calls Mastodon.
type Client struct {
	HTTP *http.Client

	mu            sync.Mutex
	onDeprecation func(endpoint, value string)
}

// SetDeprecationHandler sets what happens when a response has a Deprecation header.
func (c *Client) SetDeprecationHandler(fn func(endpoint, value string)) {
	c.mu.Lock()
	c.onDeprecation = fn
	c.mu.Unlock()
}

var idSegment = regexp.MustCompile(`^\d+$`)

// endpoint is the method and the path without the query, with numeric segments as :id.
func endpoint(method, path string) string {
	path, _, _ = strings.Cut(path, "?")
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if i > 0 && idSegment.MatchString(s) {
			segs[i] = ":id"
		}
	}
	return method + " " + strings.Join(segs, "/")
}

type callOptions struct {
	method         string
	token          string
	body           any
	idempotencyKey string
}

func (c *Client) call(ctx context.Context, base, path string, opts callOptions, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	method := opts.method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if opts.body != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(opts.body); err != nil {
			return err
		}
		body = bytes.NewReader(bytes.TrimRight(buf.Bytes(), "\n"))
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return netx.FetchError(err)
	}
	req.Header.Set("User-Agent", mastorss.UserAgent())
	req.Header.Set("Accept", "application/json")
	if opts.token != "" {
		req.Header.Set("Authorization", "Bearer "+opts.token)
	}
	if opts.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if opts.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", opts.idempotencyKey)
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return netx.FetchError(err)
	}
	defer res.Body.Close()
	if dep := res.Header.Get("Deprecation"); dep != "" {
		c.mu.Lock()
		fn := c.onDeprecation
		c.mu.Unlock()
		if fn != nil {
			fn(endpoint(method, path), dep)
		}
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return netx.FetchError(err)
	}
	text := string(raw)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		detail := js.Slice16(text, 0, 200)
		var j any
		if json.Unmarshal(raw, &j) == nil {
			if m, ok := j.(map[string]any); ok && m["error"] != nil {
				detail = jsString(m["error"])
			}
		}
		msg := fmt.Sprintf("HTTP %d", res.StatusCode)
		if detail != "" {
			msg += ": " + detail
		}
		var reset int64
		if ms, ok := js.ParseDate(res.Header.Get("X-RateLimit-Reset")); ok {
			reset = ms
		}
		return &Error{Status: res.StatusCode, Message: msg, ResetAt: reset}
	}
	if text == "" {
		text = "{}"
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return netx.Named("SyntaxError", err.Error())
	}
	return nil
}

// jsString is String(v) for a value from JSON.
func jsString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return js.FormatNumber(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			if x != nil {
				parts[i] = jsString(x)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	}
	return ""
}

// Instance reads the instance's version and limits. Needs no token.
func (c *Client) Instance(ctx context.Context, base string) (*Instance, error) {
	var j struct {
		Version     *string `json:"version"`
		APIVersions *struct {
			Mastodon *int `json:"mastodon"`
		} `json:"api_versions"`
		Configuration *struct {
			Statuses *struct {
				MaxCharacters            *int `json:"max_characters"`
				CharactersReservedPerURL *int `json:"characters_reserved_per_url"`
			} `json:"statuses"`
		} `json:"configuration"`
	}
	if err := c.call(ctx, base, "/api/v2/instance", callOptions{}, &j); err != nil {
		return nil, err
	}
	info := &Instance{Version: "unknown", MaxCharacters: 500, CharactersPerURL: 23}
	if j.Version != nil {
		info.Version = *j.Version
	}
	if j.APIVersions != nil && j.APIVersions.Mastodon != nil {
		info.APIVersion = *j.APIVersions.Mastodon
	}
	if j.Configuration != nil && j.Configuration.Statuses != nil {
		if v := j.Configuration.Statuses.MaxCharacters; v != nil {
			info.MaxCharacters = *v
		}
		if v := j.Configuration.Statuses.CharactersReservedPerURL; v != nil {
			info.CharactersPerURL = *v
		}
	}
	return info, nil
}

// VerifyApp returns the app behind the token and its scopes. Needs no scope since Mastodon 4.3.
func (c *Client) VerifyApp(ctx context.Context, base, token string) (*App, error) {
	var j struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := c.call(ctx, base, "/api/v1/apps/verify_credentials", callOptions{token: token}, &j); err != nil {
		return nil, err
	}
	if j.Scopes == nil {
		j.Scopes = []string{}
	}
	return &App{Name: j.Name, Scopes: j.Scopes}, nil
}

// CanPost reports whether the scopes are enough to post: write or write:statuses.
func CanPost(scopes []string) bool {
	for _, s := range scopes {
		if s == "write" || s == "write:statuses" {
			return true
		}
	}
	return false
}

// VerifyAccount reads the account behind the token.
func (c *Client) VerifyAccount(ctx context.Context, base, token string) (*Account, error) {
	var j struct {
		ID             any     `json:"id"`
		Acct           any     `json:"acct"`
		URL            any     `json:"url"`
		FollowersCount *int    `json:"followers_count"`
		StatusesCount  *int    `json:"statuses_count"`
		LastStatusAt   *string `json:"last_status_at"`
		Bot            *bool   `json:"bot"`
	}
	if err := c.call(ctx, base, "/api/v1/accounts/verify_credentials", callOptions{token: token}, &j); err != nil {
		return nil, err
	}
	a := &Account{ID: jsString(j.ID), Acct: jsString(j.Acct), URL: jsString(j.URL)}
	if j.FollowersCount != nil {
		a.Followers = *j.FollowersCount
	}
	if j.StatusesCount != nil {
		a.Posts = *j.StatusesCount
	}
	if j.LastStatusAt != nil {
		a.LastStatusAt = *j.LastStatusAt
	}
	if j.Bot != nil {
		a.Bot = *j.Bot
	}
	return a, nil
}

// Post publishes a post with an Idempotency-Key.
func (c *Client) Post(ctx context.Context, base, token string, params StatusParams, idempotencyKey string) (*Posted, error) {
	var j struct {
		ID  any     `json:"id"`
		URL *string `json:"url"`
		URI *string `json:"uri"`
	}
	opts := callOptions{method: http.MethodPost, token: token, idempotencyKey: idempotencyKey, body: params}
	if err := c.call(ctx, base, "/api/v1/statuses", opts, &j); err != nil {
		return nil, err
	}
	p := &Posted{ID: jsString(j.ID)}
	if j.URL != nil {
		p.URL = *j.URL
	} else if j.URI != nil {
		p.URL = *j.URI
	}
	return p, nil
}

// RecentStatuses returns the account's latest own posts, without replies and boosts.
func (c *Client) RecentStatuses(ctx context.Context, base, token, accountID string) ([]Recent, error) {
	var list []struct {
		URL     *string `json:"url"`
		URI     *string `json:"uri"`
		Content *string `json:"content"`
		Card    *struct {
			URL *string `json:"url"`
		} `json:"card"`
	}
	path := "/api/v1/accounts/" + js.EncodeURIComponent(accountID) + "/statuses?limit=40&exclude_replies=true&exclude_reblogs=true"
	if err := c.call(ctx, base, path, callOptions{token: token}, &list); err != nil {
		return nil, err
	}
	out := make([]Recent, 0, len(list))
	for _, s := range list {
		var r Recent
		if s.URL != nil {
			r.URL = *s.URL
		} else if s.URI != nil {
			r.URL = *s.URI
		}
		if s.Content != nil {
			r.Content = *s.Content
		}
		if s.Card != nil && s.Card.URL != nil {
			r.Card = *s.Card.URL
		}
		out = append(out, r)
	}
	return out, nil
}
