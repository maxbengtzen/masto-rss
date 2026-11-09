package poll

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbengtzen/masto-rss/internal/config"
	"github.com/maxbengtzen/masto-rss/internal/feed"
	"github.com/maxbengtzen/masto-rss/internal/format"
	"github.com/maxbengtzen/masto-rss/internal/store"
	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

var (
	NOW    = time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC).UnixMilli()
	LIMITS = format.Limits{MaxCharacters: 500, CharactersPerURL: 23}
)

const HOUR = int64(3600_000)

func ptr(v int64) *int64 { return &v }

func item(n int, hoursAgo ...int64) *feed.Item {
	i := &feed.Item{Keys: []string{fmt.Sprintf("g:%d", n)}, Title: fmt.Sprintf("Post %d", n)}
	if len(hoursAgo) > 0 {
		i.Published = ptr(NOW - hoursAgo[0]*HOUR)
	}
	return i
}

func titles(items []*feed.Item) []string {
	out := []string{}
	for _, i := range items {
		out = append(out, i.Title)
	}
	return out
}

func TestDecideFirstRun(t *testing.T) {
	items := []*feed.Item{item(3, 1), item(2, 2), item(1, 3)}
	opts := Options{MaxAgeMs: 48 * HOUR, MaxPostsPerPoll: 5}
	d := Decide(items, []bool{false, false, false}, true, opts, NOW, nil)
	if d.Reason != "first-run" || len(d.Post) != 0 || len(d.Skipped) != 3 {
		t.Errorf("fick %+v", d)
	}
	opts.Backfill = 2
	d = Decide(items, []bool{false, false, false}, true, opts, NOW, nil)
	if !reflect.DeepEqual(titles(d.Post), []string{"Post 2", "Post 3"}) || !reflect.DeepEqual(titles(d.Skipped), []string{"Post 1"}) {
		t.Errorf("fick %v %v", titles(d.Post), titles(d.Skipped))
	}
}

func TestDecideOldestFirstWithCap(t *testing.T) {
	var items []*feed.Item
	for n := 8; n >= -1; n-- {
		items = append(items, item(n, int64(9-n)))
	}
	seen := make([]bool, len(items))
	for i := range seen {
		seen[i] = i >= 3 // 8, 7 och 6 är nya
	}
	d := Decide(items, seen, false, Options{MaxAgeMs: 48 * HOUR, MaxPostsPerPoll: 2}, NOW, nil)
	if d.Reason != "" || !reflect.DeepEqual(titles(d.Post), []string{"Post 6", "Post 7"}) || !reflect.DeepEqual(titles(d.Deferred), []string{"Post 8"}) || len(d.Skipped) != 0 {
		t.Errorf("fick %+v", d)
	}
}

func TestDecideTooOldAndUndated(t *testing.T) {
	items := []*feed.Item{item(4), item(3, 1), item(2, 50), item(1, 2)}
	none := []bool{false, false, false, false}
	d := Decide(items, none, false, Options{MaxAgeMs: 48 * HOUR, MaxPostsPerPoll: 5}, NOW, nil)
	if !reflect.DeepEqual(titles(d.Skipped), []string{"Post 2"}) || !reflect.DeepEqual(titles(d.Post), []string{"Post 1", "Post 3", "Post 4"}) {
		t.Errorf("fick %v %v", titles(d.Skipped), titles(d.Post))
	}
	if off := Decide(items, none, false, Options{MaxPostsPerPoll: 5}, NOW, nil); len(off.Skipped) != 0 {
		t.Error("MAX_AGE=0 ska inte hoppa över något")
	}
}

func TestDecideAnomaly(t *testing.T) {
	var items []*feed.Item
	for i := 0; i < 10; i++ {
		items = append(items, item(i, int64(i)))
	}
	seen := make([]bool, 10)
	for i := range seen {
		seen[i] = i < 4 // sex av tio nya
	}
	d := Decide(items, seen, false, Options{MaxPostsPerPoll: 5}, NOW, nil)
	if d.Reason != "anomaly" || len(d.Skipped) != 6 || len(d.Post) != 0 {
		t.Errorf("fick %+v", d)
	}
	// Ett litet flöde med mest nytt är normalt.
	if d := Decide(items[:4], []bool{false, false, false, true}, false, Options{MaxPostsPerPoll: 5}, NOW, nil); d.Reason != "" {
		t.Error("litet flöde")
	}
}

func TestDecideNewerThanSeenIsNotAnomaly(t *testing.T) {
	opts := Options{MaxAgeMs: 48 * HOUR, MaxPostsPerPoll: 5}
	var items, undated []*feed.Item
	for i := 0; i < 10; i++ {
		items = append(items, item(i, int64(i)))
		undated = append(undated, item(i))
	}
	none := make([]bool, 10)
	d := Decide(items, none, false, opts, NOW, ptr(NOW-10*HOUR))
	if d.Reason != "" || !reflect.DeepEqual(titles(d.Post), []string{"Post 9", "Post 8", "Post 7", "Post 6", "Post 5"}) || len(d.Deferred) != 5 {
		t.Errorf("fick %+v", d)
	}
	if Decide(items, none, false, opts, NOW, ptr(NOW)).Reason != "anomaly" {
		t.Error("inte nyare: avvikelse")
	}
	if Decide(undated, none, false, opts, NOW, ptr(NOW-10*HOUR)).Reason != "anomaly" {
		t.Error("utan datum: avvikelse")
	}
	if Decide(items, none, false, opts, NOW, nil).Reason != "anomaly" {
		t.Error("okänt datum: avvikelse")
	}
}

// --- pollFeed mot en låtsad server -------------------------------------------------------

type testItem struct {
	guid, link, title string
	date              int64
}

func numbered(from, to int, newest int64) []testItem {
	var out []testItem
	for n := to; n >= from; n-- {
		out = append(out, testItem{
			guid:  fmt.Sprintf("https://example.com/?p=%d", n),
			link:  fmt.Sprintf("https://example.com/post-%d/?utm_source=rss", n),
			title: fmt.Sprintf("Post %d", n),
			date:  newest - int64(to-n)*HOUR,
		})
	}
	return out
}

func rssXML(items []testItem) string {
	var b strings.Builder
	for _, i := range items {
		b.WriteString("<item><title>" + i.title + "</title>")
		if i.link != "" {
			b.WriteString("<link>" + i.link + "</link>")
		}
		b.WriteString(`<guid isPermaLink="false">` + i.guid + "</guid>")
		b.WriteString("<pubDate>" + time.UnixMilli(i.date).UTC().Format(http.TimeFormat) + "</pubDate></item>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Testflöde</title><link>https://example.com/</link><language>sv-SE</language>` + b.String() + "</channel></rss>"
}

type feedServer struct {
	*httptest.Server
	mu       sync.Mutex
	handler  http.HandlerFunc
	requests []*http.Request
}

func (s *feedServer) serve(items []testItem, etag string) {
	s.set(func(w http.ResponseWriter, r *http.Request) {
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		fmt.Fprint(w, rssXML(items))
	})
}

func (s *feedServer) set(h http.HandlerFunc) {
	s.mu.Lock()
	s.handler, s.requests = h, nil
	s.mu.Unlock()
}

func (s *feedServer) first() *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[0]
}

func setup(t *testing.T) (*feedServer, config.Feed, *store.Store) {
	s := &feedServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r)
		h := s.handler
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	cfg, err := config.Load([]string{"MASTODON_URL=https://mastodon.example", "FEED_NEWS_URL=" + s.URL + "/feed/", "FEED_NEWS_TOKEN=t"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return s, cfg.Feeds[0], st
}

func pollAt(t *testing.T, f config.Feed, st *store.Store, now int64, dry bool) *Result {
	t.Helper()
	r, err := Poll(context.Background(), f, st, PollOptions{DryRun: dry, Limits: LIMITS, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func feedState(t *testing.T, st *store.Store) *store.FeedState {
	s, err := st.GetFeed("news")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPollFirstRunThenOnlyNew(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 5, Date(2026, 10, 26, 12)), `"v1"`)
	r := pollAt(t, f, st, NOW, false)
	if r.Decision.Reason != "first-run" || r.Items != 5 || r.Language != "sv" {
		t.Errorf("fick %+v", r)
	}
	s.serve(numbered(1, 7, Date(2026, 10, 26, 12)), `"v2"`)
	r = pollAt(t, f, st, NOW+HOUR, false)
	if s.first().Header.Get("If-None-Match") != `"v1"` || !reflect.DeepEqual(titles(r.Decision.Post), []string{"Post 6", "Post 7"}) {
		t.Errorf("fick %v", titles(r.Decision.Post))
	}
	s.serve(numbered(1, 7, Date(2026, 10, 26, 12)), "")
	r = pollAt(t, f, st, NOW+2*HOUR, false)
	if len(r.Decision.Post) != 0 || feedState(t, st).ETag != "" {
		t.Errorf("fick %+v", r.Decision)
	}
}

// Date är Date.UTC i JavaScript, med månader från 1.
func Date(y, m, d, h int) int64 {
	return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC).UnixMilli()
}

func TestPollWholePageNewVersusChangedKeys(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 10, Date(2026, 10, 26, 12)), "")
	pollAt(t, f, st, NOW, false)
	if *feedState(t, st).NewestPublished != Date(2026, 10, 26, 12) {
		t.Error("senaste datum")
	}
	s.serve(numbered(11, 20, NOW+10*HOUR), "")
	r := pollAt(t, f, st, NOW+11*HOUR, false)
	if r.Decision.Reason != "" || !reflect.DeepEqual(titles(r.Decision.Post), []string{"Post 11", "Post 12", "Post 13", "Post 14", "Post 15"}) ||
		!reflect.DeepEqual(titles(r.Decision.Deferred), []string{"Post 16", "Post 17", "Post 18", "Post 19", "Post 20"}) {
		t.Errorf("fick %+v", r.Decision)
	}
	s.serve(numbered(11, 20, NOW+10*HOUR), "")
	r = pollAt(t, f, st, NOW+12*HOUR, false)
	if r.Decision.Reason != "" || !reflect.DeepEqual(titles(r.Decision.Post), []string{"Post 16", "Post 17", "Post 18", "Post 19", "Post 20"}) || *feedState(t, st).NewestPublished != NOW+10*HOUR {
		t.Errorf("fick %+v", r.Decision)
	}
	changed := numbered(11, 20, NOW+10*HOUR)
	for i := range changed {
		changed[i].guid += "0"
		changed[i].link += "&amp;ny=1"
	}
	s.serve(changed, "")
	r = pollAt(t, f, st, NOW+13*HOUR, false)
	if r.Decision.Reason != "anomaly" || len(r.Queued) != 0 {
		t.Errorf("fick %+v", r.Decision)
	}
}

func TestPollDeferredDoesNotMoveDate(t *testing.T) {
	s, f, st := setup(t)
	f.MaxPostsPerPoll = 1
	s.serve(numbered(1, 10, Date(2026, 10, 26, 12)), "")
	pollAt(t, f, st, NOW, false)
	for round, want := range []string{"Post 11", "Post 12", "Post 13"} {
		s.serve(numbered(11, 20, NOW+10*HOUR), "")
		r := pollAt(t, f, st, NOW+int64(11+round)*HOUR, false)
		if r.Decision.Reason != "" || !reflect.DeepEqual(titles(r.Decision.Post), []string{want}) {
			t.Errorf("varv %d: fick %+v", round, r.Decision)
		}
	}
}

func TestPollDateBelongsToURLAndFutureCountsAsNow(t *testing.T) {
	s, f, st := setup(t)
	s.serve(append(numbered(10, 10, NOW+24*HOUR), numbered(1, 9, Date(2026, 10, 26, 12))...), "")
	pollAt(t, f, st, NOW, false)
	if *feedState(t, st).NewestPublished != NOW {
		t.Error("ett datum i framtiden räknas som nu")
	}
	moved := f
	moved.URL = s.URL + "/ny-feed/"
	s.serve(numbered(11, 20, NOW+10*HOUR), "")
	r := pollAt(t, moved, st, NOW+11*HOUR, false)
	if r.Decision.Reason != "anomaly" || *feedState(t, st).NewestPublished != NOW+10*HOUR {
		t.Errorf("fick %+v", r.Decision)
	}
}

func TestPoll304KeepsState(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 3, Date(2026, 10, 26, 12)), `"v1"`)
	pollAt(t, f, st, NOW, false)
	s.set(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(304) })
	r := pollAt(t, f, st, NOW+HOUR, false)
	state := feedState(t, st)
	if !r.Unchanged || state.ETag != `"v1"` || *state.CheckedAt != NOW+HOUR || *state.PrimedAt != NOW {
		t.Errorf("fick %+v %+v", r, state)
	}
}

func TestPollDryRunSavesNothing(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 3, Date(2026, 10, 26, 12)), `"v1"`)
	r := pollAt(t, f, st, NOW, true)
	if r.Decision.Reason != "first-run" || feedState(t, st) != nil {
		t.Error("torrläget sparade")
	}
	if ok, _ := st.IsSeen("news", []string{"g:https://example.com/?p=1"}); ok {
		t.Error("nyckel sparad")
	}
}

func TestPollImportedLinksCountAsSeen(t *testing.T) {
	s, f, st := setup(t)
	st.Transaction(false, func(tx *store.Store) error {
		_, err := tx.ImportKeys("news", f.URL, []string{"l:example.com/post-1", "l:example.com/post-2"}, NOW-24*HOUR)
		return err
	})
	s.serve([]testItem{
		{"https://example.com/?p=3", "https://example.com/post-3/?utm_source=x", "Post 3", NOW - HOUR},
		{"https://example.com/?p=2", "https://www.example.com/post-2/?utm_campaign=y", "Post 2", NOW - 2*HOUR},
		{"https://example.com/?p=1", "https://example.com/post-1", "Post 1", NOW - 3*HOUR},
	}, "")
	r := pollAt(t, f, st, NOW, false)
	if r.Decision.Reason != "" || !reflect.DeepEqual(titles(r.Decision.Post), []string{"Post 3"}) {
		t.Errorf("fick %+v", r.Decision)
	}
	// GUID-nyckeln sparas nu också, så att en ändrad länk senare inte ger en dubblett.
	if ok, _ := st.IsSeen("news", []string{"g:https://example.com/?p=2"}); !ok {
		t.Error("GUID inte sparat")
	}
}

func TestPollDeferredNextRoundWithoutETag(t *testing.T) {
	s, f, st := setup(t)
	f.MaxPostsPerPoll = 1
	s.serve(numbered(1, 10, Date(2026, 10, 26, 12)), `"v1"`)
	pollAt(t, f, st, NOW, false)
	s.serve(numbered(1, 13, Date(2026, 10, 26, 12)+3*HOUR), `"v2"`)
	r := pollAt(t, f, st, NOW, false)
	if !reflect.DeepEqual(titles(r.Decision.Post), []string{"Post 11"}) || !reflect.DeepEqual(titles(r.Decision.Deferred), []string{"Post 12", "Post 13"}) || feedState(t, st).ETag != "" {
		t.Errorf("fick %+v", r.Decision)
	}
	s.serve(numbered(1, 13, Date(2026, 10, 26, 12)+3*HOUR), `"v2"`)
	r = pollAt(t, f, st, NOW+HOUR, false)
	if s.first().Header.Get("If-None-Match") != "" || !reflect.DeepEqual(titles(r.Decision.Post), []string{"Post 12"}) {
		t.Errorf("fick %+v", r.Decision)
	}
}

func TestPollFailuresCountedAndReset(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 2, Date(2026, 10, 26, 12)), "")
	pollAt(t, f, st, NOW, false)
	s.set(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		fmt.Fprint(w, "fel")
	})
	for _, want := range []int{1, 2} {
		_, err := Poll(context.Background(), f, st, PollOptions{Limits: LIMITS, Now: NOW})
		var pe *Error
		if !errors.As(err, &pe) || pe.Failures != want || err.Error() != "HTTP 502 Bad Gateway" {
			t.Errorf("fick %v", err)
		}
	}
	if feedState(t, st).Failures != 2 {
		t.Error("fel i rad")
	}
	s.serve(numbered(1, 2, Date(2026, 10, 26, 12)), "")
	r := pollAt(t, f, st, NOW, false)
	if feedState(t, st).Failures != 0 || r.PreviousFailures != 2 {
		t.Errorf("nollställdes inte: %d", r.PreviousFailures)
	}
}

func TestPollErrorOnNewFeedDoesNotPrime(t *testing.T) {
	s, f, st := setup(t)
	s.set(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>nej</html>") })
	if _, err := Poll(context.Background(), f, st, PollOptions{Limits: LIMITS, Now: NOW}); err == nil {
		t.Fatal("väntade fel")
	}
	if feedState(t, st).PrimedAt != nil {
		t.Error("blev inläst")
	}
	s.serve(numbered(1, 3, Date(2026, 10, 26, 12)), "")
	if r := pollAt(t, f, st, NOW, false); r.Decision.Reason != "first-run" {
		t.Error("ska vara nytt flöde")
	}
}

func TestPollNewURLNoOldETag(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 2, Date(2026, 10, 26, 12)), `"v1"`)
	pollAt(t, f, st, NOW, false)
	s.serve(numbered(1, 2, Date(2026, 10, 26, 12)), "")
	moved := f
	moved.URL = s.URL + "/ny-feed/"
	pollAt(t, moved, st, NOW, false)
	if s.first().Header.Get("If-None-Match") != "" {
		t.Error("skickade gamla ETag")
	}
}

func TestPollKeysForgottenAfter30Days(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 3, Date(2026, 10, 26, 12)), "")
	pollAt(t, f, st, NOW, false)
	s.serve(numbered(3, 3, Date(2026, 10, 26, 12)), "")
	pollAt(t, f, st, NOW+ForgetAfterMs-HOUR, false)
	if ok, _ := st.IsSeen("news", []string{"g:https://example.com/?p=1"}); !ok {
		t.Error("glömdes för tidigt")
	}
	pollAt(t, f, st, NOW+ForgetAfterMs+HOUR, false)
	if ok, _ := st.IsSeen("news", []string{"g:https://example.com/?p=1"}); ok {
		t.Error("glömdes inte")
	}
	if ok, _ := st.IsSeen("news", []string{"g:https://example.com/?p=3"}); !ok {
		t.Error("det som finns kvar glömdes")
	}
}

func TestPollSamePostTwice(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 1, Date(2026, 10, 26, 12)), "")
	pollAt(t, f, st, NOW, false)
	twice := numbered(2, 2, Date(2026, 10, 26, 12))
	s.serve(append(append(twice, twice...), numbered(1, 1, Date(2026, 10, 26, 12))...), "")
	if r := pollAt(t, f, st, NOW, false); !reflect.DeepEqual(titles(r.Decision.Post), []string{"Post 2"}) {
		t.Errorf("fick %v", titles(r.Decision.Post))
	}
}

func TestPollQueuesFinishedText(t *testing.T) {
	s, f, st := setup(t)
	s.serve(numbered(1, 2, Date(2026, 10, 26, 12)), "")
	pollAt(t, f, st, NOW, false)
	s.serve(numbered(1, 3, Date(2026, 10, 26, 12)), "")
	dry := pollAt(t, f, st, NOW, true)
	if len(dry.Queued) != 1 || dry.Queued[0].Status != "Post 3\n\nhttps://example.com/post-3/?utm_source=rss" {
		t.Errorf("fick %+v", dry.Queued)
	}
	if due, _ := st.DuePosts("news", NOW); len(due) != 0 {
		t.Error("torrläget lade i utkorgen")
	}
	s.serve(numbered(1, 3, Date(2026, 10, 26, 12)), "")
	live := pollAt(t, f, st, NOW, false)
	due, _ := st.DuePosts("news", NOW)
	if len(live.Queued) != 1 || len(due) != 1 || due[0].ID != PostID("news", "g:https://example.com/?p=3") || due[0].Language != "sv" ||
		due[0].Status != "Post 3\n\nhttps://example.com/post-3/?utm_source=rss" {
		t.Errorf("fick %+v", due)
	}
}

func TestPostIDGolden(t *testing.T) {
	var golden struct {
		PostID []struct{ Feed, Key, ID string }
	}
	testutil.Golden(t, "text.json", &golden)
	for _, c := range golden.PostID {
		if got := PostID(c.Feed, c.Key); got != c.ID {
			t.Errorf("PostID(%q, %q) = %s, väntade %s", c.Feed, c.Key, got, c.ID)
		}
	}
}
