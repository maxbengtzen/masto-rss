package mastodon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

// server svarar med status och body (JSON om det inte är en sträng), och sparar anropet.
func server(t *testing.T, status int, body any, headers map[string]string) (*httptest.Server, *http.Request, *[]byte) {
	var last http.Request
	var sent []byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = *r
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		sent = buf[:n]
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		if s, ok := body.(string); ok {
			fmt.Fprint(w, s)
		} else {
			json.NewEncoder(w).Encode(body)
		}
	}))
	t.Cleanup(s.Close)
	return s, &last, &sent
}

func TestInstance(t *testing.T) {
	s, req, _ := server(t, 200, map[string]any{"version": "4.7.3", "api_versions": map[string]any{"mastodon": 11},
		"configuration": map[string]any{"statuses": map[string]any{"max_characters": 1000, "characters_reserved_per_url": 23}}}, nil)
	c := &Client{}
	info, err := c.Instance(ctx, s.URL)
	if err != nil || *info != (Instance{"4.7.3", 11, 1000, 23}) {
		t.Errorf("fick %+v, %v", info, err)
	}
	if req.URL.Path != "/api/v2/instance" || req.Header.Get("Authorization") != "" {
		t.Errorf("anrop %s %v", req.URL, req.Header)
	}
	s2, _, _ := server(t, 200, map[string]any{"version": "0.22.3 git-abc"}, nil)
	info, _ = c.Instance(ctx, s2.URL)
	if *info != (Instance{"0.22.3 git-abc", 0, 500, 23}) {
		t.Errorf("standardvärden: %+v", info)
	}
}

func TestVerifyAppCanPostVerifyAccount(t *testing.T) {
	s, req, _ := server(t, 200, map[string]any{"name": "masto-rss", "scopes": []string{"read", "profile", "write"}}, nil)
	c := &Client{}
	app, err := c.VerifyApp(ctx, s.URL, "hemlig")
	if err != nil || !reflect.DeepEqual(app, &App{"masto-rss", []string{"read", "profile", "write"}}) || req.Header.Get("Authorization") != "Bearer hemlig" {
		t.Errorf("fick %+v, %v", app, err)
	}
	if !CanPost(app.Scopes) || !CanPost([]string{"write:statuses"}) || CanPost([]string{"read", "write:media"}) {
		t.Error("CanPost")
	}
	s2, _, _ := server(t, 200, map[string]any{"id": "1", "acct": "feber", "url": "https://mastodon.example/@feber", "followers_count": 195,
		"statuses_count": 36350, "last_status_at": "2026-10-26", "bot": true}, nil)
	acc, err := c.VerifyAccount(ctx, s2.URL, "hemlig")
	if err != nil || *acc != (Account{"1", "feber", "https://mastodon.example/@feber", 195, 36350, "2026-10-26", true}) {
		t.Errorf("fick %+v, %v", acc, err)
	}
}

func TestPostSendsTextSettingsAndKey(t *testing.T) {
	s, req, sent := server(t, 200, map[string]any{"id": "99", "url": "https://mastodon.example/@feber/99"}, nil)
	c := &Client{}
	posted, err := c.Post(ctx, s.URL, "hemlig", StatusParams{Status: "Hej\n\nhttps://example.com", Visibility: "public", Language: "sv", QuotePolicy: "nobody"}, "nyckel-1")
	if err != nil || *posted != (Posted{"99", "https://mastodon.example/@feber/99"}) {
		t.Errorf("fick %+v, %v", posted, err)
	}
	if req.URL.Path != "/api/v1/statuses" || req.Header.Get("Idempotency-Key") != "nyckel-1" || !strings.HasPrefix(req.Header.Get("User-Agent"), "masto-rss/") {
		t.Errorf("anrop %s %v", req.URL, req.Header)
	}
	if string(*sent) != `{"status":"Hej\n\nhttps://example.com","visibility":"public","language":"sv","quote_approval_policy":"nobody"}` {
		t.Errorf("body %s", *sent)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	cases := []struct {
		status  int
		body    any
		message string
		kind    string
	}{
		{401, map[string]any{"error": "The access token is invalid"}, "HTTP 401: The access token is invalid", "auth"},
		{422, map[string]any{"error": "Validation failed: Text character limit of 500 exceeded"}, "HTTP 422: Validation failed: Text character limit of 500 exceeded", "rejected"},
		{502, "<html>Bad gateway</html>", "HTTP 502: <html>Bad gateway</html>", "transient"},
	}
	for _, c := range cases {
		s, _, _ := server(t, c.status, c.body, nil)
		_, err := (&Client{}).Post(ctx, s.URL, "t", StatusParams{Status: "x", Visibility: "public"}, "k")
		var me *Error
		if !errors.As(err, &me) || me.Error() != c.message || me.Kind() != c.kind {
			t.Errorf("%d: fick %v", c.status, err)
		}
	}
}

func TestRateLimitReset(t *testing.T) {
	s, _, _ := server(t, 429, map[string]any{"error": "Too many requests"}, map[string]string{"X-RateLimit-Reset": "2026-10-26T12:05:00.150Z"})
	_, err := (&Client{}).Post(ctx, s.URL, "t", StatusParams{Status: "x", Visibility: "public"}, "k")
	var me *Error
	want := time.Date(2026, 10, 26, 12, 5, 0, 150e6, time.UTC).UnixMilli()
	if !errors.As(err, &me) || me.Kind() != "transient" || me.ResetAt != want {
		t.Errorf("fick %+v", err)
	}
}

func TestDeprecationHeaderReported(t *testing.T) {
	s, _, _ := server(t, 200, map[string]any{"version": "5.0.0"}, map[string]string{"Deprecation": "@1700000000"})
	var seen []string
	c := &Client{}
	c.SetDeprecationHandler(func(endpoint, value string) { seen = append(seen, endpoint+" "+value) })
	c.Instance(ctx, s.URL)
	if !reflect.DeepEqual(seen, []string{"GET /api/v2/instance @1700000000"}) {
		t.Errorf("fick %v", seen)
	}
	if got := endpoint("GET", "/api/v1/accounts/42/statuses?limit=40"); got != "GET /api/v1/accounts/:id/statuses" {
		t.Errorf("endpoint: %q", got)
	}
}
