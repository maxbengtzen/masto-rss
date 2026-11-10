package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mastorss "github.com/maxbengtzen/masto-rss"
	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

// Programmet körs här mot en låtsad server som är både flöde, Mastodon och Signal-tjänst, med
// samma svar som när TypeScript-versionens körningar spelades in (scripts/golden.ts). Loggen,
// statussidan, inläggen och aviseringarna ska bli desamma. Inget nätverk.

type fake struct {
	*httptest.Server
	mu      sync.Mutex
	posts   []fakePost
	signals []string
	count   int
	feedXML func(path string) string // nil = rss(1, 3, nu \u2212 1 min)
}

type fakePost struct {
	Token string         `json:"token"`
	Key   string         `json:"key"`
	Body  map[string]any `json:"body"`
}

type fakeAccount struct {
	id, acct  string
	followers int
	scopes    []string
	bot       bool
}

var fakeAccounts = map[string]fakeAccount{
	"bra":  {"1", "alfa", 12345, []string{"read", "write"}, true},
	"bra3": {"3", "delta", 0, []string{"write:statuses"}, false},
	"las":  {"2", "gamma", 7, []string{"read"}, true},
}

// rss är flödet i golden.ts: poster from..to, nyast först, en timme mellan varje.
func rss(from, to int, newest time.Time, path string) string {
	var items []string
	for n := to; n >= from; n-- {
		date := newest.Add(-time.Duration(to-n) * time.Hour).UTC().Format(http.TimeFormat)
		items = append(items, fmt.Sprintf(`<item><title>Post %d från %s &amp; @anna</title><link>https://example.com%s/post-%d/?utm_source=rss</link>`+
			`<guid isPermaLink="false">https://example.com%s/?p=%d</guid><pubDate>%s</pubDate>`+
			`<description><![CDATA[<p>Sammanfattning %d</p>]]></description></item>`, n, path, path, n, path, n, date, n))
	}
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Flöde ` + path + `</title><language>sv-SE</language>` +
		strings.Join(items, "\n") + `</channel></rss>`
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(status int, body any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(body)
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		acc, authorized := fakeAccounts[token]
		path, _, _ := strings.Cut(r.RequestURI, "?")
		switch r.Method + " " + path {
		case "GET /old":
			w.Header().Set("Location", "/feed-d")
			w.WriteHeader(301)
		case "GET /feed-a", "GET /feed-c", "GET /feed-d", "GET /feed":
			w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
			body := rss(1, 3, time.Now().Add(-time.Minute), path)
			if f.feedXML != nil {
				body = f.feedXML(path)
			}
			io.WriteString(w, body)
		case "GET /broken":
			w.WriteHeader(500)
			io.WriteString(w, "trasigt")
		case "GET /api/v2/instance":
			send(200, map[string]any{"version": "4.7.3", "api_versions": map[string]any{"mastodon": 11},
				"configuration": map[string]any{"statuses": map[string]any{"max_characters": 500, "characters_reserved_per_url": 23}}})
		case "GET /api/v1/apps/verify_credentials":
			if !authorized {
				send(401, map[string]any{"error": "The access token is invalid"})
				return
			}
			send(200, map[string]any{"name": "test", "scopes": acc.scopes})
		case "GET /api/v1/accounts/verify_credentials":
			if !authorized {
				send(401, map[string]any{"error": "The access token is invalid"})
				return
			}
			if token == "bra3" {
				w.Header().Set("Deprecation", "@1700000000")
			}
			send(200, map[string]any{"id": acc.id, "acct": acc.acct, "url": f.URL + "/@" + acc.acct, "followers_count": acc.followers,
				"statuses_count": 10, "last_status_at": time.Now().Format("2006-01-02"), "bot": acc.bot})
		case "POST /api/v1/statuses":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if !authorized {
				send(401, map[string]any{"error": "The access token is invalid"})
				return
			}
			if s, _ := body["status"].(string); strings.HasPrefix(s, "Post 2 ") {
				send(422, map[string]any{"error": "Validation failed: Text character limit of 500 exceeded"})
				return
			}
			f.mu.Lock()
			f.posts = append(f.posts, fakePost{Token: token, Key: r.Header.Get("Idempotency-Key"), Body: body})
			f.count++
			n := f.count
			f.mu.Unlock()
			send(200, map[string]any{"id": fmt.Sprint(n), "url": fmt.Sprintf("%s/@%s/%d", f.URL, acc.acct, n)})
		case "POST /v2/send":
			var body struct{ Message string }
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.signals = append(f.signals, body.Message)
			f.mu.Unlock()
			send(201, map[string]any{})
		default:
			send(404, map[string]any{"error": "Not found"})
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// freePort ger en ledig port för statussidan.
func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func (f *fake) port() string { return f.URL[strings.LastIndex(f.URL, ":")+1:] }

// watcher är stdout, som säger till när en rad som matchar dyker upp.
type watcher struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	re    *regexp.Regexp
	found chan struct{}
}

func (w *watcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if w.re != nil && w.re.Match(w.buf.Bytes()) {
		w.re = nil
		close(w.found)
	}
	return len(p), nil
}

func (w *watcher) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

var timestamp = regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d `)

type runResult struct {
	Status int    `json:"status"`
	Out    string `json:"out"`
	Err    string `json:"err"`
}

type runner struct {
	t    *testing.T
	fake *fake
	root string
}

// clean tar bort tidsstämplar och byter porten och datakatalogen mot fasta ord, som i golden.ts.
func (r *runner) clean(s string) string {
	s = versionless(timestamp.ReplaceAllString(s, ""), mastorss.Version())
	return strings.ReplaceAll(strings.ReplaceAll(s, r.fake.port(), "PORT"), r.root, "DATA")
}

// goldenVersion är versionen när golden-filerna spelades in.
const goldenVersion = "1.0.3"

// versionless byter versionsnumret mot ordet VERSION, så att golden-filerna gäller för varje version.
func versionless(s, version string) string {
	for _, format := range []string{"masto-rss %s ", `"version":"%s"`, "masto-rss/%s"} {
		s = strings.ReplaceAll(s, fmt.Sprintf(format, version), fmt.Sprintf(format, "VERSION"))
	}
	return s
}

func (r *runner) run(env []string, args []string, opts Options) runResult {
	var stdout, stderr bytes.Buffer
	if opts.StartDelay == nil {
		opts.StartDelay = func(i int) time.Duration { return time.Duration(i) * 50 * time.Millisecond }
	}
	if opts.Sleep == nil {
		opts.Sleep = func(time.Duration) {}
	}
	if opts.HeartbeatFile == "" {
		opts.HeartbeatFile = filepath.Join(r.root, "alive")
	}
	code := Main(args, env, &stdout, &stderr, opts)
	return runResult{Status: code, Out: r.clean(stdout.String()), Err: r.clean(stderr.String())}
}

func (r *runner) env(dir string, extra ...string) []string {
	base := []string{
		"DATA_DIR=" + filepath.Join(r.root, dir), "MASTODON_URL=" + r.fake.URL, "SIGNAL_API_URL=" + r.fake.URL, "SIGNAL_NUMBER=+46700000000",
		"FEED_ALPHA_URL=" + r.fake.URL + "/feed-a", "FEED_ALPHA_TOKEN=bra", "FEED_ALPHA_BACKFILL=2",
		"FEED_BETA_URL=" + r.fake.URL + "/broken", "FEED_BETA_TOKEN=bra", "FEED_BETA_LANGUAGE=en", "FAIL_NOTIFY_AFTER=1",
		"FEED_GAMMA_URL=" + r.fake.URL + "/feed-c", "FEED_GAMMA_TOKEN=las",
		"FEED_DELTA_URL=" + r.fake.URL + "/old", "FEED_DELTA_TOKEN=bra3", `FEED_DELTA_TEMPLATE={feed}: {title} ({author})\n{summary}\n{link}`,
		"FEED_PAUSED_ENABLED=false",
	}
	return append(base, extra...)
}

func TestRunsGolden(t *testing.T) {
	testutil.Stockholm(t)
	var golden struct {
		Runs      map[string]runResult
		Status    json.RawMessage
		StatusRaw string
		Posts     []fakePost
		Signals   []string
	}
	testutil.Golden(t, "runs.json", &golden)
	fk := newFake(t)
	r := &runner{t: t, fake: fk, root: t.TempDir()}
	got := map[string]runResult{}

	// Hela programmet med statussida, tills det fjärde flödet är klart, och sedan SIGTERM.
	stdout := &watcher{re: regexp.MustCompile(`delta: (inget nytt|nytt flöde)`), found: make(chan struct{})}
	signals := make(chan os.Signal, 1)
	addr := make(chan string, 1)
	var statusRaw string
	done := make(chan int)
	go func() {
		done <- Main(nil, r.env("live", "STATUS_PORT="+freePort(t)), stdout, io.Discard, Options{
			StartDelay:    func(i int) time.Duration { return time.Duration(i) * 50 * time.Millisecond },
			Sleep:         func(time.Duration) {},
			HeartbeatFile: filepath.Join(r.root, "alive"),
			Signals:       signals,
			Listening:     func(a string) { addr <- a },
		})
	}()
	select {
	case <-stdout.found:
	case <-time.After(20 * time.Second):
		t.Fatalf("delta blev aldrig klar:\n%s", stdout.String())
	}
	res, err := http.Get("http://" + <-addr + "/status")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type %q", ct)
	}
	statusRaw = regexp.MustCompile(`"lastPost":"[^"]+"`).ReplaceAllString(r.clean(string(b)), `"lastPost":"TID"`)
	signals <- syscall.SIGTERM
	got["live"] = runResult{Status: <-done, Out: r.clean(stdout.String())}

	got["again"] = r.run(r.env("live", "ONCE=1", "STATUS_PORT=0"), nil, Options{})
	got["dryNew"] = r.run(r.env("dry", "DRY_RUN=1", "ONCE=1", "STATUS_PORT=0", "LOG_LEVEL=debug"), nil, Options{})
	importFile := filepath.Join(r.root, "processed_entries.txt")
	os.WriteFile(importFile, []byte("\ufeffhttps://example.com/feed-a/post-1/?utm_source=rss\r\nhttps://www.example.com/feed-a/post-2/\n\n  \n"), 0o644)
	got["import"] = r.run(r.env("imp", "ONCE=1", "STATUS_PORT=0"), []string{"import", "ALPHA", importFile}, Options{})
	got["afterImport"] = r.run(r.env("imp", "DRY_RUN=1", "ONCE=1", "STATUS_PORT=0"), nil, Options{})
	got["postDry"] = r.run(r.env("post", "DRY_RUN=1", "STATUS_PORT=0"), []string{"post", "alla", "Hej", `allihop!\nRad två`}, Options{})
	got["post"] = r.run(r.env("post", "STATUS_PORT=0"), []string{"post", "--language", "sv", "alpha,delta", `Hej\n\nallihop!`}, Options{})
	got["postUsage"] = r.run(r.env("post", "STATUS_PORT=0"), []string{"post", "saknas", "text"}, Options{})
	got["importUsage"] = r.run(r.env("post", "STATUS_PORT=0"), []string{"import", "alpha"}, Options{})
	got["unknown"] = r.run(r.env("post", "STATUS_PORT=0"), []string{"okänt"}, Options{})
	got["noConfig"] = r.run([]string{"DATA_DIR=" + filepath.Join(r.root, "none")}, nil, Options{})
	got["badConfig"] = r.run([]string{"DATA_DIR=" + filepath.Join(r.root, "none"), "FEED_X_URL=nej", "FEED_X_TOKEN=t", "MASTODON_URL=" + fk.URL, "STATUS_PORT=x"}, nil, Options{})
	got["allPaused"] = r.run([]string{"DATA_DIR=" + filepath.Join(r.root, "paused"), "FEED_X_ENABLED=no", "ONCE=1"}, nil, Options{})

	for name, want := range golden.Runs {
		// Avsiktlig skillnad: användningstexterna nämner programmet, inte "node src/main.ts".
		want.Err = strings.ReplaceAll(want.Err, "node src/main.ts", "masto-rss")
		want.Out, want.Err = versionless(want.Out, goldenVersion), versionless(want.Err, goldenVersion)
		if got[name] != want {
			t.Errorf("körningen %s skiljer sig:\n--- fick (slutkod %d)\n%s%s\n--- väntade (slutkod %d)\n%s%s", name,
				got[name].Status, got[name].Out, got[name].Err, want.Status, want.Out, want.Err)
		}
	}
	if golden.StatusRaw = versionless(golden.StatusRaw, goldenVersion); statusRaw != golden.StatusRaw {
		t.Errorf("statussidan:\nfick    %s\nväntade %s", statusRaw, golden.StatusRaw)
	}

	// Inläggen: flödets inlägg har samma nyckel som i TypeScript-versionen. Nyckeln för `post`
	// beror på instansens adress (med porten) och räknas fram här.
	fk.mu.Lock()
	defer fk.mu.Unlock()
	if len(fk.posts) != len(golden.Posts) {
		t.Fatalf("%d inlägg, väntade %d", len(fk.posts), len(golden.Posts))
	}
	for i, want := range golden.Posts {
		p := fk.posts[i]
		if i > 0 {
			sum := sha256.Sum256([]byte("masto-rss post\n" + fk.URL + "\n" + p.Token + "\n" + "Hej\n\nallihop!"))
			want.Key = hex.EncodeToString(sum[:])[:32]
		}
		if !reflect.DeepEqual(p, want) {
			t.Errorf("inlägg %d:\nfick    %+v\nväntade %+v", i, p, want)
		}
	}
	sort.Strings(fk.signals)
	if !reflect.DeepEqual(fk.signals, golden.Signals) {
		t.Errorf("aviseringar:\nfick    %q\nväntade %q", fk.signals, golden.Signals)
	}
}

// Ett flöde som inte går att tolka ger fel och slutkoden 1, och nästa försök om ett intervall.
func TestUnparseableFeed(t *testing.T) {
	fk := newFake(t)
	fk.feedXML = func(string) string { return "<html>inget flöde</html>" }
	r := &runner{t: t, fake: fk, root: t.TempDir()}
	res := r.run([]string{"DATA_DIR=" + r.root, "STATUS_PORT=0", "MASTODON_URL=" + fk.URL, "FEED_NEWS_URL=" + fk.URL + "/feed", "FEED_NEWS_TOKEN=bra", "ONCE=1"}, nil, Options{})
	if res.Status != 1 || !strings.Contains(res.Out, "news: FEL (1 i rad): DetectError: Unrecognized feed format; nästa försök om 15 min") {
		t.Errorf("slutkod %d:\n%s", res.Status, res.Out)
	}
}

// Ett riktigt varv postar det nya, en gång, med nyckel, språk och synlighet.
func TestPostsOnce(t *testing.T) {
	fk := newFake(t)
	r := &runner{t: t, fake: fk, root: t.TempDir()}
	env := func(extra ...string) []string {
		return append([]string{"DATA_DIR=" + r.root, "STATUS_PORT=0", "MASTODON_URL=" + fk.URL, "FEED_NEWS_URL=" + fk.URL + "/feed", "FEED_NEWS_TOKEN=bra", "ONCE=1"}, extra...)
	}
	fk.feedXML = func(path string) string { return rss(1, 3, time.Now(), path) }
	if res := r.run(env("BACKFILL=1"), nil, Options{}); res.Status != 0 || !strings.Contains(res.Out, "news: postat ") {
		t.Fatalf("slutkod %d:\n%s", res.Status, res.Out)
	}
	fk.feedXML = func(path string) string { return rss(1, 4, time.Now(), path) }
	r.run(env(), nil, Options{})
	r.run(env(), nil, Options{})
	if len(fk.posts) != 2 {
		t.Fatalf("%d inlägg", len(fk.posts))
	}
	want := map[string]any{"status": "Post 4 från /feed & @\u200banna\n\nhttps://example.com/feed/post-4/?utm_source=rss", "visibility": "public", "language": "sv"}
	if !reflect.DeepEqual(fk.posts[1].Body, want) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(fk.posts[1].Key) {
		t.Errorf("fick %+v", fk.posts[1])
	}
}

func TestHeartbeatAndStatus404(t *testing.T) {
	fk := newFake(t)
	root := t.TempDir()
	signals := make(chan os.Signal, 1)
	addr := make(chan string, 1)
	done := make(chan int)
	alive := filepath.Join(root, "alive")
	go func() {
		done <- Main(nil, []string{"DATA_DIR=" + root, "STATUS_PORT=" + freePort(t), "MASTODON_URL=" + fk.URL, "FEED_NEWS_URL=" + fk.URL + "/feed", "FEED_NEWS_TOKEN=bra"},
			io.Discard, io.Discard, Options{HeartbeatFile: alive, Signals: signals, Listening: func(a string) { addr <- a }, StartDelay: func(int) time.Duration { return time.Hour }})
	}()
	a := <-addr
	for _, path := range []string{"/annat", "/status?x=1"} {
		res, err := http.Get("http://" + a + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("%s: %d", path, res.StatusCode)
		}
	}
	if _, err := os.Stat(alive); err != nil {
		t.Errorf("hälsofilen: %v", err)
	}
	signals <- os.Interrupt
	if code := <-done; code != 0 {
		t.Errorf("slutkod %d", code)
	}
}
