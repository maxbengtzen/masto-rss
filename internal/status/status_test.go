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

// ago och daysAgo ska skriva samma text som TypeScript-versionen.
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
			t.Errorf("Ago(%d) = %q, väntade %q", c.Delta, got, c.Text)
		}
	}
	for _, c := range golden.DaysAgo {
		if got := DaysAgo(c.Date, c.Now, stockholm); got != c.Text {
			t.Errorf("DaysAgo(%q, %d) = %q, väntade %q", c.Date, c.Now, got, c.Text)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	for n, want := range map[int]string{0: "0", 195: "195", 1234: "1\u00A0234", 12345: "12\u00A0345", 1234567: "1\u00A0234\u00A0567"} {
		if got := FormatNumber(n); got != want {
			t.Errorf("FormatNumber(%d) = %q, väntade %q", n, got, want)
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
		t.Errorf("huvuden: %v", res.Header)
	}
	if string(body) != `{"version":"1.0.0","totals":{"feeds":1,"postedLastDay":2,"errors":0},"feeds":[]}` {
		t.Errorf("fick %s", body)
	}
	var back Snapshot
	if json.Unmarshal(body, &back) != nil {
		t.Error("inte JSON")
	}
	res, _ = s.Client().Get(s.URL + "/annat")
	if res.StatusCode != 404 {
		t.Errorf("fick %d", res.StatusCode)
	}
}

func TestDashboard(t *testing.T) {
	ok := Feed{Account: "@nyheter", URL: "https://mastodon.example/@nyheter", OK: true, Summary: "195 följare · 12 min sedan"}
	bad := Feed{Account: "@blogg", URL: "https://mastodon.example/@blogg", Summary: "fel: flödet svarar inte (3 i rad)", Problem: "flödet svarar inte (3 i rad)"}
	snapshot := Snapshot{Totals: Totals{Feeds: 2, PostedLastDay: 7, Errors: 1}, Feeds: []Feed{ok, bad}}

	d := ToDashboard(snapshot)
	want := []Metric{{"konton", 2}, {"senaste dygnet", 7}, {"fel", 1}}
	if len(d.Metrics) != len(want) {
		t.Fatalf("metrics: %v", d.Metrics)
	}
	for i := range want {
		if d.Metrics[i] != want[i] {
			t.Errorf("metric %d = %v, väntade %v", i, d.Metrics[i], want[i])
		}
	}
	if d.Status != "warn" || d.Message != "@blogg: flödet svarar inte (3 i rad)" {
		t.Errorf("status %q, %q", d.Status, d.Message)
	}
	// Flödet som felar kommer först.
	if len(d.Items) != 2 || d.Items[0].Name != "@blogg" || d.Items[1] != (Item{"@nyheter", ok.Summary, ok.URL}) {
		t.Errorf("items: %+v", d.Items)
	}

	bad2 := bad
	bad2.Account = "@arkiv"
	if d := ToDashboard(Snapshot{Feeds: []Feed{bad, ok, bad2}}); d.Message != "2 flöden felar: @blogg, @arkiv" {
		t.Errorf("fick %q", d.Message)
	}

	// Utan fel: ok och inget meddelande. Bara http(s)-länkar, högst åtta rader.
	var many []Feed
	for range 10 {
		many = append(many, Feed{Account: "@x", URL: "javascript:alert(1)"})
	}
	d = ToDashboard(Snapshot{Feeds: many})
	if d.Status != "ok" || d.Message != "" || len(d.Items) != 8 || d.Items[0].Href != "" {
		t.Errorf("fick %+v", d)
	}
}

func TestHandlerDashboard(t *testing.T) {
	snapshot := Snapshot{Totals: Totals{Feeds: 1, PostedLastDay: 2}, Feeds: []Feed{{Account: "@nyheter", URL: "https://mastodon.example/@nyheter", OK: true, Summary: "nyss"}}}
	s := httptest.NewServer(Handler(func() Snapshot { return snapshot }))
	defer s.Close()
	res, err := s.Client().Get(s.URL + "/api/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("fick %d, %v", res.StatusCode, res.Header)
	}
	want := `{"metrics":[{"k":"konton","v":1},{"k":"senaste dygnet","v":2},{"k":"fel","v":0}],"items":[{"name":"@nyheter","label":"nyss","href":"https://mastodon.example/@nyheter"}],"status":"ok"}`
	if string(body) != want {
		t.Errorf("fick %s", body)
	}
	req, _ := http.NewRequest(http.MethodPost, s.URL+"/api/dashboard", nil)
	if res, _ := s.Client().Do(req); res.StatusCode != 404 {
		t.Errorf("POST gav %d", res.StatusCode)
	}
}
