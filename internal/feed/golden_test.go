package feed

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

// Golden-filerna är TypeScript-versionens svar (feedsmith + normaliseringen i feed.ts), inspelade
// inför omskrivningen. Go-versionen ska ge samma.

func TestParseGolden(t *testing.T) {
	parseGolden(t, testutil.Testdata("golden", "feeds.json"), testutil.Testdata("fixtures"), 18)
}

// Sparade riktiga flöden, bara lokalt.
func TestParseGoldenLocal(t *testing.T) {
	dir := testutil.Local(t)
	parseGolden(t, filepath.Join(dir, "golden", "feeds.json"), filepath.Join(dir, "fixtures"), 1)
}

func parseGolden(t *testing.T, goldenFile, fixtures string, minFeeds int) {
	testutil.Stockholm(t)
	var golden map[string]json.RawMessage
	testutil.ReadJSON(t, goldenFile, &golden)
	if len(golden) < minFeeds {
		t.Fatalf("för få flöden i golden-filen: %d", len(golden))
	}
	for file, want := range golden {
		b, err := os.ReadFile(filepath.Join(fixtures, file))
		if err != nil {
			t.Fatal(err)
		}
		contentType := "application/rss+xml"
		if len(file) > 5 && file[len(file)-5:] == ".json" {
			contentType = "application/feed+json"
		}
		parsed, err := Parse(DecodeBody(b, contentType), "https://example.com/feeds/"+file)
		var got any
		if err != nil {
			got = map[string]any{"error": describe(err)}
		} else {
			got = parsed
		}
		compareJSON(t, file, got, want)
	}
}

func describe(err error) string {
	if n, ok := err.(interface{ Name() string }); ok {
		return n.Name() + ": " + err.Error()
	}
	return "Error: " + err.Error()
}

// compareJSON jämför v (som JSON) med want, oavsett nyckelordning.
func compareJSON(t *testing.T, name string, v any, want json.RawMessage) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got, exp any
	json.Unmarshal(b, &got)
	json.Unmarshal(want, &exp)
	// Orsaken till ett MalformedError är fast-xml-parsers text och jämförs inte.
	if m, ok := exp.(map[string]any); ok {
		delete(m, "cause")
	}
	if !reflect.DeepEqual(got, exp) {
		g, _ := json.MarshalIndent(got, "", " ")
		e, _ := json.MarshalIndent(exp, "", " ")
		t.Errorf("%s:\nfick     %s\nväntade  %s", name, g, e)
	}
}

func TestTextGolden(t *testing.T) {
	var golden struct {
		NormalizeLink  []struct{ Link, Key string }
		DecodeEntities []struct{ HTML, Text string }
		HTMLToText     []struct{ HTML, Text string }
		DecodeBody     []struct {
			Hex         string
			ContentType *string
			Text        string
		}
	}
	testutil.Golden(t, "text.json", &golden)
	for _, c := range golden.NormalizeLink {
		if got := NormalizeLink(c.Link); got != c.Key {
			t.Errorf("NormalizeLink(%q) = %q, väntade %q", c.Link, got, c.Key)
		}
	}
	for _, c := range golden.DecodeEntities {
		if got := DecodeEntities(c.HTML); got != c.Text {
			t.Errorf("DecodeEntities(%q) = %q, väntade %q", c.HTML, got, c.Text)
		}
	}
	for _, c := range golden.HTMLToText {
		if got := HTMLToText(c.HTML); got != c.Text {
			t.Errorf("HTMLToText(%q) = %q, väntade %q", c.HTML, got, c.Text)
		}
	}
	for _, c := range golden.DecodeBody {
		b, _ := hex.DecodeString(c.Hex)
		ct := ""
		if c.ContentType != nil {
			ct = *c.ContentType
		}
		if got := DecodeBody(b, ct); got != c.Text {
			t.Errorf("DecodeBody(%s, %q) = %q, väntade %q", c.Hex, ct, got, c.Text)
		}
	}
}
