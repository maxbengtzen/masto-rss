package format

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

var mastodon = Limits{MaxCharacters: 500, CharactersPerURL: 23}

func TestLength(t *testing.T) {
	cases := []struct {
		text   string
		limits Limits
		want   int
	}{
		{"hej", mastodon, 3},
		{"läs https://example.com/en/mycket/lang/adress?utm_source=rss här", mastodon, 4 + 23 + 4},
		{"👨\u200D👩\u200D👧 åäö", mastodon, 5},
		// Utan känd länklängd räknas länken i sin helhet.
		{"https://example.com/a", Limits{MaxCharacters: 500}, 21},
	}
	for _, c := range cases {
		if got := Length(c.text, c.limits); got != c.want {
			t.Errorf("Length(%q) = %d, väntade %d", c.text, got, c.want)
		}
	}
}

func TestDefuseMentions(t *testing.T) {
	for in, want := range map[string]string{
		"Tack @anna och @bo@example.social!":  "Tack @\u200Banna och @\u200Bbo@example.social!",
		"@först":                              "@\u200Bförst",
		"mejla info@example.com":              "mejla info@example.com",
		"https://medium.com/@skribent/inlagg": "https://medium.com/@skribent/inlagg",
	} {
		if got := DefuseMentions(in); got != want {
			t.Errorf("DefuseMentions(%q) = %q, väntade %q", in, got, want)
		}
	}
}

func TestRender(t *testing.T) {
	fields := Fields{Title: "Rubrik", Link: "https://example.com/a", Summary: "Kort text", Author: "Anna", Feed: "Sajten"}
	cases := map[string]string{
		"{title}\n\n{link}":                             "Rubrik\n\nhttps://example.com/a",
		"{feed}: {title} ({author})\n{summary}\n{link}": "Sajten: Rubrik (Anna)\nKort text\nhttps://example.com/a",
	}
	for tpl, want := range cases {
		if got := Render(tpl, fields, mastodon); got != want {
			t.Errorf("Render(%q) = %q, väntade %q", tpl, got, want)
		}
	}
	noSummary := fields
	noSummary.Summary = ""
	if got := Render("{title}\n\n{summary}\n\n{link}", noSummary, mastodon); got != "Rubrik\n\nhttps://example.com/a" {
		t.Errorf("tom sammanfattning: %q", got)
	}
	if got := Render("{title}\n\n{link}", Fields{Link: "https://example.com/a"}, mastodon); got != "https://example.com/a" {
		t.Errorf("tom titel: %q", got)
	}
}

func TestRenderShortens(t *testing.T) {
	link := "https://example.com/en/lang/adress"
	small := Limits{MaxCharacters: 60, CharactersPerURL: 23}
	text := Render("{title}\n{summary}\n{link}", Fields{Title: "Rubrik", Summary: strings.Repeat("x", 100), Link: link}, small)
	if !strings.HasSuffix(text, "…\n"+link) || !strings.HasPrefix(text, "Rubrik\nxxx") || Length(text, small) != 60 {
		t.Errorf("sammanfattningen kortas först: %q (%d)", text, Length(text, small))
	}
	long := Render("{title}\n{link}", Fields{Title: strings.Repeat("Ord ", 30), Link: link}, small)
	if !strings.HasSuffix(long, "…\n"+link) || Length(long, small) > 60 {
		t.Errorf("sedan titeln, aldrig länken: %q", long)
	}
}

// Text och längd ska bli desamma som i TypeScript-versionen för alla kombinationer i golden-filen.
func TestFormatGolden(t *testing.T) {
	formatGolden(t, testutil.Testdata("golden", "format.json"))
}

// Text från sparade riktiga flöden, bara lokalt.
func TestFormatGoldenLocal(t *testing.T) {
	formatGolden(t, filepath.Join(testutil.Local(t), "golden", "format.json"))
}

func formatGolden(t *testing.T, path string) {
	var golden struct {
		Render []struct {
			Template string
			Fields   struct{ Title, Link, Summary, Author, Feed string }
			Limits   struct {
				MaxCharacters    int
				CharactersPerURL int `json:"charactersPerUrl"`
			}
			Out    string
			Length int
		}
		Lengths []struct {
			Text              string
			Length, Graphemes int
			Defused           string
		}
	}
	testutil.ReadJSON(t, path, &golden)
	if len(golden.Render) < 100 {
		t.Fatalf("för få fall: %d", len(golden.Render))
	}
	for _, c := range golden.Render {
		limits := Limits{MaxCharacters: c.Limits.MaxCharacters, CharactersPerURL: c.Limits.CharactersPerURL}
		got := Render(c.Template, Fields(c.Fields), limits)
		if got != c.Out {
			t.Errorf("Render(%q, %+v, %+v)\nfick    %q\nväntade %q", c.Template, c.Fields, limits, got, c.Out)
			continue
		}
		if n := Length(got, limits); n != c.Length {
			t.Errorf("Length(%q) = %d, väntade %d", got, n, c.Length)
		}
	}
	for _, c := range golden.Lengths {
		if n := Length(c.Text, mastodon); n != c.Length {
			t.Errorf("Length(%q) = %d, väntade %d", c.Text, n, c.Length)
		}
		if n := Length(c.Text, Limits{MaxCharacters: 500}); n != c.Graphemes {
			t.Errorf("grafem i %q: %d, väntade %d", c.Text, n, c.Graphemes)
		}
		if d := DefuseMentions(c.Text); d != c.Defused {
			t.Errorf("DefuseMentions(%q) = %q, väntade %q", c.Text, d, c.Defused)
		}
	}
}

// Grafemklustren ska följa Unicodes egna testfall för samma version.
func TestGraphemeBreakConformance(t *testing.T) {
	f, err := os.Open(testutil.Testdata("unicode", "GraphemeBreakTest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var want []string
		var cluster strings.Builder
		var input strings.Builder
		for _, field := range strings.Fields(line)[1:] {
			switch field {
			case "÷":
				want = append(want, cluster.String())
				cluster.Reset()
			case "×":
			default:
				cp, _ := strconv.ParseUint(field, 16, 32)
				cluster.WriteRune(rune(cp))
				input.WriteRune(rune(cp))
			}
		}
		got := graphemes(input.String())
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: fick %q, väntade %q", line, got, want)
		}
		n++
	}
	if n < 500 {
		t.Fatalf("för få testfall: %d", n)
	}
	if unicodeVersion == "" {
		t.Fatal("ingen Unicode-version")
	}
}
