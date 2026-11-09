package deliver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbengtzen/masto-rss/internal/config"
	"github.com/maxbengtzen/masto-rss/internal/mastodon"
	"github.com/maxbengtzen/masto-rss/internal/store"
)

var NOW = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC).UnixMilli()

type fakeMastodon struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	keys   []string
	paths  []string
	reply  func(n int, w http.ResponseWriter)
}

func newMastodon(t *testing.T, reply func(n int, w http.ResponseWriter)) *fakeMastodon {
	m := &fakeMastodon{reply: reply}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.paths = append(m.paths, r.RequestURI)
		if r.Method == http.MethodPost {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.bodies = append(m.bodies, body)
			m.keys = append(m.keys, r.Header.Get("Idempotency-Key"))
			n := len(m.bodies)
			m.mu.Unlock()
			m.reply(n, w)
			return
		}
		m.mu.Unlock()
		// Kontots senaste inlägg: ett som länkar till a.
		json.NewEncoder(w).Encode([]any{map[string]any{"url": "https://mastodon.example/@news/1",
			"content": `<p>A <a href="https://example.com/a/?utm_source=rss&amp;utm_medium=feed">länk</a></p>`, "card": nil}})
	}))
	t.Cleanup(m.Close)
	return m
}

func jsonReply(status int, body any) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}
}

func setup(t *testing.T, m *fakeMastodon) (config.Feed, *store.Store) {
	cfg, err := config.Load([]string{"MASTODON_URL=" + m.URL, "FEED_NEWS_URL=https://example.com/feed/", "FEED_NEWS_TOKEN=t", "FEED_NEWS_QUOTE_POLICY=followers"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return cfg.Feeds[0], st
}

func queue(st *store.Store, id, title string, createdAt int64) {
	st.Enqueue(store.Post{ID: id, Feed: "news", Title: title, Link: "https://example.com/" + id + "/", Status: title + "\n\nhttps://example.com/" + id + "/", Language: "sv"}, createdAt)
}

var opts = Options{Now: func() int64 { return NOW }, Sleep: func(time.Duration) {}}

func run(t *testing.T, f config.Feed, st *store.Store, acc Account, o Options) *Result {
	t.Helper()
	res, err := Deliver(context.Background(), &mastodon.Client{}, f, st, acc, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestOldestFirstWithKeyLanguageAndQuotes(t *testing.T) {
	m := newMastodon(t, func(n int, w http.ResponseWriter) {
		json.NewEncoder(w).Encode(map[string]any{"id": n, "url": "https://mastodon.example/@news/" + string(rune('0'+n))})
	})
	f, st := setup(t, m)
	queue(st, "a", "Första", NOW-2000)
	queue(st, "b", "Andra", NOW-1000)
	res := run(t, f, st, Account{Quotes: true}, opts)
	if len(res.Posted) != 2 || res.Posted[0].Post.Title != "Första" || res.Posted[0].URL != "https://mastodon.example/@news/1" || res.Posted[1].URL != "https://mastodon.example/@news/2" {
		t.Errorf("fick %+v", res.Posted)
	}
	if !reflect.DeepEqual(m.keys, []string{"a", "b"}) {
		t.Errorf("nycklar %v", m.keys)
	}
	want := map[string]any{"status": "Första\n\nhttps://example.com/a/", "visibility": "public", "language": "sv", "quote_approval_policy": "followers"}
	if !reflect.DeepEqual(m.bodies[0], want) {
		t.Errorf("body %v", m.bodies[0])
	}
	if p, _ := st.GetPost("a"); p.State != "posted" {
		t.Error("a inte postad")
	}
	if due, _ := st.DuePosts("news", NOW); len(due) != 0 {
		t.Error("kvar i utkorgen")
	}
}

func TestNoQuotePolicyWhenUnsupported(t *testing.T) {
	m := newMastodon(t, jsonReply(200, map[string]any{"id": "1", "url": "u"}))
	f, st := setup(t, m)
	queue(st, "a", "Första", NOW)
	run(t, f, st, Account{Quotes: false}, opts)
	if _, ok := m.bodies[0]["quote_approval_policy"]; ok {
		t.Error("citatinställningen skickades")
	}
}

func TestRejectedFailsButNextIsSent(t *testing.T) {
	m := newMastodon(t, func(n int, w http.ResponseWriter) {
		if n == 1 {
			jsonReply(422, map[string]any{"error": "Validation failed"})(n, w)
		} else {
			jsonReply(200, map[string]any{"id": "2", "url": "u2"})(n, w)
		}
	})
	f, st := setup(t, m)
	queue(st, "a", "För långt", NOW-2000)
	queue(st, "b", "Okej", NOW-1000)
	res := run(t, f, st, Account{Quotes: true}, opts)
	if len(res.Failed) != 1 || res.Failed[0].Post.Title != "För långt" || res.Failed[0].Error != "HTTP 422: Validation failed" {
		t.Errorf("fick %+v", res.Failed)
	}
	if len(res.Posted) != 1 || res.Posted[0].Post.Title != "Okej" {
		t.Errorf("fick %+v", res.Posted)
	}
	if p, _ := st.GetPost("a"); p.State != "failed" {
		t.Error("a inte misslyckad")
	}
}

func TestTransientAnd429Retry(t *testing.T) {
	m := newMastodon(t, func(_ int, w http.ResponseWriter) {
		w.WriteHeader(503)
		w.Write([]byte("upptagen"))
	})
	f, st := setup(t, m)
	queue(st, "a", "Första", NOW-2000)
	queue(st, "b", "Andra", NOW-1000)
	res := run(t, f, st, Account{Quotes: true}, opts)
	if res.Retry == nil || res.Retry.NextAttemptAt != NOW+60_000 || len(m.bodies) != 1 {
		t.Fatalf("fick %+v", res.Retry)
	}
	if p, _ := st.GetPost("a"); p.Attempts != 1 {
		t.Error("försök")
	}
	if due, _ := st.DuePosts("news", NOW); len(due) != 1 || due[0].ID != "b" {
		t.Errorf("väntande %+v", due)
	}

	reset := "2026-10-26T12:30:00Z"
	m.reply = func(_ int, w http.ResponseWriter) {
		w.Header().Set("X-RateLimit-Reset", reset)
		jsonReply(429, map[string]any{"error": "Too many requests"})(0, w)
	}
	later := Options{Now: func() int64 { return NOW + 60_000 }, Sleep: func(time.Duration) {}}
	res = run(t, f, st, Account{Quotes: true}, later)
	want, _ := time.Parse(time.RFC3339, reset)
	if res.Retry == nil || res.Retry.Post.ID != "a" || res.Retry.NextAttemptAt != want.UnixMilli() {
		t.Errorf("fick %+v", res.Retry)
	}
}

func TestDeniedTokenStopsAccount(t *testing.T) {
	m := newMastodon(t, jsonReply(401, map[string]any{"error": "The access token was revoked"}))
	f, st := setup(t, m)
	queue(st, "a", "Första", NOW)
	res := run(t, f, st, Account{Quotes: true}, opts)
	if res.AuthError == nil || res.AuthError.Status != 401 {
		t.Errorf("fick %+v", res)
	}
	if p, _ := st.GetPost("a"); p.State != "pending" {
		t.Error("ska vänta kvar")
	}
}

func TestAfterAnHourAccountIsCheckedFirst(t *testing.T) {
	m := newMastodon(t, jsonReply(200, map[string]any{"id": "7", "url": "https://mastodon.example/@news/7"}))
	f, st := setup(t, m)
	queue(st, "a", "Kom fram ändå", NOW-IdempotencyWindowMs-1000)
	queue(st, "b", "Kom aldrig fram", NOW-IdempotencyWindowMs-500)
	st.MarkRetry("a", NOW, "timeout")
	st.MarkRetry("b", NOW, "timeout")
	res := run(t, f, st, Account{ID: "42", Quotes: true}, opts)
	if len(res.Posted) != 2 || res.Posted[0].Post.ID != "a" || !res.Posted[0].AlreadyThere || res.Posted[1].AlreadyThere {
		t.Errorf("fick %+v", res.Posted)
	}
	if len(m.bodies) != 1 {
		t.Errorf("%d inlägg", len(m.bodies))
	}
	found := false
	for _, p := range m.paths {
		found = found || strings.HasPrefix(p, "/api/v1/accounts/42/statuses")
	}
	if !found {
		t.Errorf("kontrollerade inte kontot: %v", m.paths)
	}
}

func TestExpiredNotSent(t *testing.T) {
	m := newMastodon(t, jsonReply(200, map[string]any{"id": "1", "url": "u"}))
	f, st := setup(t, m)
	queue(st, "a", "Gammalt", NOW-f.MaxAgeMs-1000)
	res := run(t, f, st, Account{Quotes: true}, opts)
	if len(res.Expired) != 1 || len(m.bodies) != 0 {
		t.Errorf("fick %+v", res)
	}
	if p, _ := st.GetPost("a"); p.State != "failed" {
		t.Error("ska vara misslyckad")
	}
}
