package status

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

// Ago and DaysAgo must write the text recorded in the golden file.
func TestAgoGolden(t *testing.T) {
	stockholm := testutil.Stockholm(t)
	var golden struct {
		Ago []struct {
			Delta int64
			Text  string
		}
		DaysAgo []struct {
			Now        int64
			Date, Text string
		}
	}
	testutil.Golden(t, "text.json", &golden)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC).UnixMilli()
	for _, c := range golden.Ago {
		if got := Ago(now-c.Delta, now); got != c.Text {
			t.Errorf("Ago(%d) = %q, want %q", c.Delta, got, c.Text)
		}
	}
	for _, c := range golden.DaysAgo {
		if got := DaysAgo(c.Date, c.Now, stockholm); got != c.Text {
			t.Errorf("DaysAgo(%q, %d) = %q, want %q", c.Date, c.Now, got, c.Text)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	for n, want := range map[int]string{0: "0", 195: "195", 1234: "1,234", 12345: "12,345", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := FormatNumber(n); got != want {
			t.Errorf("FormatNumber(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFollowers(t *testing.T) {
	for n, want := range map[int]string{0: "0 followers", 1: "1 follower", 2: "2 followers", 195: "195 followers", 12345: "12,345 followers"} {
		if got := Followers(n); got != want {
			t.Errorf("Followers(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestHandler(t *testing.T) {
	snapshot := Snapshot{Version: "1.0.0", Totals: Totals{Feeds: 1, PostedLastDay: 2}, Feeds: []Feed{}}
	s := httptest.NewServer(Handler(func() Snapshot { return snapshot }))
	defer s.Close()
	res, err := s.Client().Get(s.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.Header.Get("Content-Type") != "application/json; charset=utf-8" || res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", res.Header)
	}
	if string(body) != `{"version":"1.0.0","totals":{"feeds":1,"postedLastDay":2,"errors":0},"feeds":[]}` {
		t.Errorf("got %s", body)
	}
	var back Snapshot
	if json.Unmarshal(body, &back) != nil {
		t.Error("not JSON")
	}
	res, _ = s.Client().Get(s.URL + "/other")
	if res.StatusCode != 404 {
		t.Errorf("got %d", res.StatusCode)
	}
}

func TestDashboard(t *testing.T) {
	ok := Feed{Account: "@news", URL: "https://mastodon.example/@news", OK: true, Summary: "195 followers · 12 min ago"}
	bad := Feed{Account: "@blog", URL: "https://mastodon.example/@blog", Summary: "error: feed not responding (3 in a row)", Problem: "feed not responding (3 in a row)"}
	snapshot := Snapshot{Totals: Totals{Feeds: 2, PostedLastDay: 7, Errors: 1}, Feeds: []Feed{ok, bad}}

	d := ToDashboard(snapshot)
	want := []Metric{{"accounts", 2}, {"last 24 h", 7}, {"errors", 1}}
	if len(d.Metrics) != len(want) {
		t.Fatalf("metrics: %v", d.Metrics)
	}
	for i := range want {
		if d.Metrics[i] != want[i] {
			t.Errorf("metric %d = %v, want %v", i, d.Metrics[i], want[i])
		}
	}
	if d.Status != "warn" || d.Message != "@blog: feed not responding (3 in a row)" {
		t.Errorf("status %q, %q", d.Status, d.Message)
	}
	// The failing feed comes first.
	if len(d.Items) != 2 || d.Items[0].Name != "@blog" || d.Items[1] != (Item{"@news", ok.Summary, ok.URL}) {
		t.Errorf("items: %+v", d.Items)
	}

	bad2 := bad
	bad2.Account = "@archive"
	if d := ToDashboard(Snapshot{Feeds: []Feed{bad, ok, bad2}}); d.Message != "2 feeds failing: @blog, @archive" {
		t.Errorf("got %q", d.Message)
	}

	// No errors: ok and no message. Only http(s) links, at most eight rows.
	var many []Feed
	for range 10 {
		many = append(many, Feed{Account: "@x", URL: "javascript:alert(1)"})
	}
	d = ToDashboard(Snapshot{Feeds: many})
	if d.Status != "ok" || d.Message != "" || len(d.Items) != 8 || d.Items[0].Href != "" {
		t.Errorf("got %+v", d)
	}
}

func TestHandlerDashboard(t *testing.T) {
	snapshot := Snapshot{Totals: Totals{Feeds: 1, PostedLastDay: 2}, Feeds: []Feed{{Account: "@news", URL: "https://mastodon.example/@news", OK: true, Summary: "just now"}}}
	s := httptest.NewServer(Handler(func() Snapshot { return snapshot }))
	defer s.Close()
	res, err := s.Client().Get(s.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("got %d, %v", res.StatusCode, res.Header)
	}
	want := `{"metrics":[{"k":"accounts","v":1},{"k":"last 24 h","v":2},{"k":"errors","v":0}],"items":[{"name":"@news","label":"just now","href":"https://mastodon.example/@news"}],"status":"ok"}`
	if string(body) != want {
		t.Errorf("got %s", body)
	}
	req, _ := http.NewRequest(http.MethodPost, s.URL+"/api/dashboard", nil)
	if res, _ := s.Client().Do(req); res.StatusCode != 404 {
		t.Errorf("POST returned %d", res.StatusCode)
	}
}
