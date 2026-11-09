// Package format bygger inläggets text: mallen fylls i, `@` i text från flödet neutraliseras, och
// texten kortas tills den ryms så som Mastodon räknar: det man ser som ett tecken (grafemkluster)
// räknas som ett, och varje länk som ett fast antal tecken. Länkar kortas aldrig.
package format

import (
	"regexp"
	"strings"

	"github.com/maxbengtzen/masto-rss/internal/js"
)

// Limits är instansens gränser.
type Limits struct {
	MaxCharacters int
	// CharactersPerURL är tecken per länk. 0 = okänt: då räknas länkar i full längd, vilket är det
	// försiktiga valet.
	CharactersPerURL int
}

// Fields är det som kan stå i mallen.
type Fields struct {
	Title   string
	Link    string
	Summary string
	Author  string
	Feed    string
}

// urlRE är /https?:\/\/[^\s<>"]+/g, med JavaScripts \s.
var urlRE = regexp.MustCompile(`https?://[^\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}<>"]+`)

// Length räknar som Mastodon.
func Length(text string, limits Limits) int {
	length, last := 0, 0
	for _, m := range urlRE.FindAllStringIndex(text, -1) {
		length += graphemeCount(text[last:m[0]])
		if limits.CharactersPerURL > 0 {
			length += limits.CharactersPerURL
		} else {
			length += graphemeCount(text[m[0]:m[1]])
		}
		last = m[1]
	}
	return length + graphemeCount(text[last:])
}

func isWord(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
}

// DefuseMentions sätter ett osynligt tecken efter `@`, så att Mastodon inte tolkar `@namn` i
// flödestexten som ett omnämnande. `@` direkt efter ett ordtecken, `/` eller `=` (e-postadresser,
// länkar) rörs inte, eftersom Mastodon inte heller tolkar dem.
func DefuseMentions(text string) string {
	if !strings.Contains(text, "@") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		b.WriteByte(c)
		if c != '@' || i+1 >= len(text) || !isWord(text[i+1]) {
			continue
		}
		if i == 0 || !isWord(text[i-1]) && text[i-1] != '/' && text[i-1] != '=' {
			b.WriteString("\u200B")
		}
	}
	return b.String()
}

// shorten ger högst max tecken, med "…" sist om något kortades bort.
func shorten(chars []string, full string, max int) string {
	if len(chars) <= max {
		return full
	}
	if max <= 1 {
		return ""
	}
	return js.TrimEnd(strings.Join(chars[:max-1], "")) + "…"
}

var manyNewlines = regexp.MustCompile(`\n{3,}`)

// tidy tar bort tomma rader i början och slutet, och låter högst en tom rad stå i följd.
func tidy(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = js.TrimEnd(line)
	}
	return js.Trim(manyNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var fieldRE = regexp.MustCompile(`\{(\w+)\}`)

// Render fyller i mallen och kortar tills texten ryms.
func Render(template string, fields Fields, limits Limits) string {
	values := map[string]string{
		"title":   DefuseMentions(fields.Title),
		"link":    fields.Link,
		"summary": DefuseMentions(fields.Summary),
		"author":  DefuseMentions(fields.Author),
		"feed":    DefuseMentions(fields.Feed),
	}
	render := func() string {
		return tidy(fieldRE.ReplaceAllStringFunc(template, func(m string) string { return values[m[1:len(m)-1]] }))
	}
	fits := func() bool { return Length(render(), limits) <= limits.MaxCharacters }

	// Korta i den här ordningen tills texten ryms. Varje fält får den största längd som fungerar.
	for _, field := range []string{"summary", "title", "author", "feed"} {
		if fits() {
			break
		}
		full := values[field]
		chars := graphemes(full)
		low, high := 0, len(chars)
		for low < high {
			mid := (low + high + 1) / 2
			values[field] = shorten(chars, full, mid)
			if fits() {
				low = mid
			} else {
				high = mid - 1
			}
		}
		values[field] = shorten(chars, full, low)
	}
	return render()
}
