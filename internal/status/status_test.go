package status

import (
	"encoding/json"
	"io"
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
