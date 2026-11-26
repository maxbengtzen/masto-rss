// Package status is the status page for a combined Homepage widget: GET /status returns JSON with
// each feed's account, followers, latest post and any errors. GET /api/dashboard returns the same in
// short form for a start page (see Dashboard). Both are meant for an internal network and show no
// tokens.
package status

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/maxbengtzen/masto-rss/internal/js"
)

// Feed is one feed's row.
type Feed struct {
	Name string `json:"name"`
	// Account is for example "@news".
	Account string `json:"account"`
	// URL is the account's profile page.
	URL       string `json:"url,omitempty"`
	Followers *int   `json:"followers,omitempty"`
	Posts     *int   `json:"posts,omitempty"`
	// LastPost is the latest post (ISO 8601), if known.
	LastPost string `json:"lastPost,omitempty"`
	OK       bool   `json:"ok"`
	// Summary is a line for Homepage, for example "195 followers · 12 min ago".
	Summary string `json:"summary"`
	// Problem is the error without prefix, "" when the feed works. It shows in /status only via Summary.
	Problem string `json:"-"`
}

// Totals are the totals. PostedLastDay is posts in the last 24 hours.
type Totals struct {
	Feeds         int `json:"feeds"`
	PostedLastDay int `json:"postedLastDay"`
	Errors        int `json:"errors"`
}

// Snapshot is the whole response.
type Snapshot struct {
	Version string `json:"version"`
	Totals  Totals `json:"totals"`
	Feeds   []Feed `json:"feeds"`
}

// Metric is a value in Dashboard: a short label and a number.
type Metric struct {
	K string `json:"k"`
	V int    `json:"v"`
}

// Item is a row in Dashboard.
type Item struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Href  string `json:"href,omitempty"`
}

// Dashboard is a short summary for a start page: at most four values, one row per feed and
// status "ok" or "warn" (with Message) when any feed is failing.
type Dashboard struct {
	Metrics []Metric `json:"metrics"`
	Items   []Item   `json:"items"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
}

// maxItems is how many feeds a start page shows.
const maxItems = 8

// ToDashboard turns a Snapshot into a Dashboard.
func ToDashboard(s Snapshot) Dashboard {
	d := Dashboard{
		Metrics: []Metric{
			{"accounts", s.Totals.Feeds},
			{"last 24 h", s.Totals.PostedLastDay},
			{"errors", s.Totals.Errors},
		},
		Items:  []Item{},
		Status: "ok",
	}
	var failing, working []Feed
	for _, f := range s.Feeds {
		if f.Problem != "" {
			failing = append(failing, f)
		} else {
			working = append(working, f)
		}
	}
	// Failing feeds first, so they show even when there are more than maxItems.
	for _, f := range slices.Concat(failing, working) {
		if len(d.Items) == maxItems {
			break
		}
		item := Item{Name: f.Account, Label: f.Summary}
		if strings.HasPrefix(f.URL, "https://") || strings.HasPrefix(f.URL, "http://") {
			item.Href = f.URL
		}
		d.Items = append(d.Items, item)
	}
	switch len(failing) {
	case 0:
	case 1:
		d.Status, d.Message = "warn", failing[0].Account+": "+failing[0].Problem
	default:
		names := make([]string, len(failing))
		for i, f := range failing {
			names[i] = f.Account
		}
		d.Status, d.Message = "warn", strconv.Itoa(len(failing))+" feeds failing: "+strings.Join(names, ", ")
	}
	return d
}

// Handler answers GET /status and GET / with the whole Snapshot, GET /api/dashboard with a
// Dashboard, and everything else with 404.
func Handler(snapshot func() Snapshot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body any
		switch r.RequestURI {
		case "/status", "/":
			body = snapshot()
		case "/api/dashboard":
			body = ToDashboard(snapshot())
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
	})
}

// Ago is how long ago, in words: "just now", "12 min ago", "3 h ago", "1 day ago", "2 days ago".
func Ago(then, now int64) string {
	minutes := floorDiv(now-then, 60_000)
	if minutes < 1 {
		return "just now"
	}
	if minutes < 60 {
		return strconv.FormatInt(minutes, 10) + " min ago"
	}
	hours := minutes / 60
	if hours < 24 {
		return strconv.FormatInt(hours, 10) + " h ago"
	}
	return daysAgo(hours / 24)
}

// daysAgo is "1 day ago" or "N days ago".
func daysAgo(days int64) string {
	if days == 1 {
		return "1 day ago"
	}
	return strconv.FormatInt(days, 10) + " days ago"
}

// Followers is the follower count with its noun: "1 follower", "12,345 followers".
func Followers(n int) string {
	if n == 1 {
		return "1 follower"
	}
	return FormatNumber(n) + " followers"
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// DaysAgo is the same for a date without a time of day (YYYY-MM-DD), counted in the time zone loc.
func DaysAgo(date string, now int64, loc *time.Location) string {
	today := time.UnixMilli(now).In(loc).Format("2006-01-02")
	t, ok1 := js.ParseDate(today)
	d, ok2 := js.ParseDate(date)
	if !ok1 || !ok2 {
		// NaN in JavaScript: Math.round(NaN) <= 0 is false, and the text becomes "NaN days ago".
		return "NaN days ago"
	}
	days := jsRound(float64(t-d) / 86_400_000)
	if days <= 0 {
		return "today"
	}
	if days == 1 {
		return "yesterday"
	}
	return daysAgo(days)
}

// jsRound is Math.round: halves round up, also for negative numbers.
func jsRound(f float64) int64 {
	i := int64(f)
	frac := f - float64(i)
	switch {
	case frac >= 0.5:
		i++
	case frac < -0.5:
		i--
	}
	return i
}

// FormatNumber writes an integer like Intl.NumberFormat("en-US"): thousands are separated by a
// comma, for example "12,345".
func FormatNumber(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
