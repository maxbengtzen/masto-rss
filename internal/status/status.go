// Package status är statussidan för en samlad Homepage-ruta: GET /status ger JSON med varje flödes
// konto, följare, senaste inlägg och eventuella fel. GET /api/dashboard ger samma sak i kort form
// för en startsida (se Dashboard). Båda är tänkta för ett internt nätverk och visar inga tokens.
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

// Feed är ett flödes rad.
type Feed struct {
	Name string `json:"name"`
	// Account är till exempel "@feber".
	Account string `json:"account"`
	// URL är kontots profilsida.
	URL       string `json:"url,omitempty"`
	Followers *int   `json:"followers,omitempty"`
	Posts     *int   `json:"posts,omitempty"`
	// LastPost är senaste inlägget (ISO 8601), om det är känt.
	LastPost string `json:"lastPost,omitempty"`
	OK       bool   `json:"ok"`
	// Summary är en rad för Homepage, till exempel "195 följare · 12 min sedan".
	Summary string `json:"summary"`
	// Problem är felet utan prefix, "" när flödet fungerar. Det syns i /status bara via Summary.
	Problem string `json:"-"`
}

// Totals är summorna. PostedLastDay är inlägg de senaste 24 timmarna.
type Totals struct {
	Feeds         int `json:"feeds"`
	PostedLastDay int `json:"postedLastDay"`
	Errors        int `json:"errors"`
}

// Snapshot är hela svaret.
type Snapshot struct {
	Version string `json:"version"`
	Totals  Totals `json:"totals"`
	Feeds   []Feed `json:"feeds"`
}

// Metric är ett värde i Dashboard: en kort etikett och ett tal.
type Metric struct {
	K string `json:"k"`
	V int    `json:"v"`
}

// Item är en rad i Dashboard.
type Item struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Href  string `json:"href,omitempty"`
}

// Dashboard är en kort sammanfattning för en startsida: högst fyra värden, en rad per flöde och
// status "ok" eller "warn" (med Message) när något flöde felar.
type Dashboard struct {
	Metrics []Metric `json:"metrics"`
	Items   []Item   `json:"items"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
}

// maxItems är hur många flöden en startsida visar.
const maxItems = 8

// ToDashboard gör om en Snapshot till en Dashboard.
func ToDashboard(s Snapshot) Dashboard {
	d := Dashboard{
		Metrics: []Metric{
			{"konton", s.Totals.Feeds},
			{"senaste dygnet", s.Totals.PostedLastDay},
			{"fel", s.Totals.Errors},
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
	// Flöden som felar först, så att de syns även när det finns fler än maxItems.
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
		d.Status, d.Message = "warn", strconv.Itoa(len(failing))+" flöden felar: "+strings.Join(names, ", ")
	}
	return d
}

// Handler svarar på GET /status och GET / med hela Snapshot, på GET /api/dashboard med en
// Dashboard, och med 404 på allt annat.
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

// Ago är hur länge sedan, på svenska: "nyss", "12 min sedan", "3 h sedan", "2 dagar sedan".
func Ago(then, now int64) string {
	minutes := floorDiv(now-then, 60_000)
	if minutes < 1 {
		return "nyss"
	}
	if minutes < 60 {
		return strconv.FormatInt(minutes, 10) + " min sedan"
	}
	hours := minutes / 60
	if hours < 24 {
		return strconv.FormatInt(hours, 10) + " h sedan"
	}
	days := hours / 24
	if days == 1 {
		return "1 dag sedan"
	}
	return strconv.FormatInt(days, 10) + " dagar sedan"
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// DaysAgo är samma sak för ett datum utan klockslag (YYYY-MM-DD), räknat i tidszonen loc.
func DaysAgo(date string, now int64, loc *time.Location) string {
	today := time.UnixMilli(now).In(loc).Format("2006-01-02")
	t, ok1 := js.ParseDate(today)
	d, ok2 := js.ParseDate(date)
	if !ok1 || !ok2 {
		// NaN i JavaScript: Math.round(NaN) <= 0 är falskt, och texten blir "NaN dagar sedan".
		return "NaN dagar sedan"
	}
	days := jsRound(float64(t-d) / 86_400_000)
	if days <= 0 {
		return "idag"
	}
	if days == 1 {
		return "igår"
	}
	return strconv.FormatInt(days, 10) + " dagar sedan"
}

// jsRound är Math.round: halva avrundas uppåt, även för negativa tal.
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

// FormatNumber skriver ett heltal som Intl.NumberFormat("sv-SE"): tusental skiljs med ett hårt
// blanksteg, till exempel "12 345".
func FormatNumber(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString("\u00A0")
		}
		b.WriteRune(c)
	}
	if neg {
		return "\u2212" + b.String()
	}
	return b.String()
}
