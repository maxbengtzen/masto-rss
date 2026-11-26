// Package format builds the post text: the template is filled in, `@` in text from the feed is
// defused, and the text is shortened until it fits the way Mastodon counts: what you see as one
// character (a grapheme cluster) counts as one, and every link as a fixed number of characters.
// Links are never shortened.
package format

import (
	"regexp"
	"strings"

	"github.com/maxbengtzen/masto-rss/internal/js"
)

// Limits are the instance's limits.
type Limits struct {
	MaxCharacters int
	// CharactersPerURL is the number of characters per link. 0 = unknown: links are then counted at
	// full length, which is the cautious choice.
	CharactersPerURL int
}

// Fields is what can appear in the template.
type Fields struct {
	Title   string
	Link    string
	Summary string
	Author  string
	Feed    string
}

// urlRE is /https?:\/\/[^\s<>"]+/g, with JavaScript's \s.
var urlRE = regexp.MustCompile(`https?://[^\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}<>"]+`)

// Length counts the way Mastodon does.
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

// DefuseMentions inserts an invisible character after `@`, so that Mastodon doesn't interpret
// `@name` in the feed text as a mention. An `@` directly after a word character, `/` or `=` (email
// addresses, links) is left alone, since Mastodon doesn't interpret those either.
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

// shorten returns at most max characters, ending in "…" if anything was cut.
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

// tidy removes blank lines at the start and end, and allows at most one blank line in a row.
func tidy(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = js.TrimEnd(line)
	}
	return js.Trim(manyNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var fieldRE = regexp.MustCompile(`\{(\w+)\}`)

// Render fills in the template and shortens until the text fits.
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

	// Shorten in this order until the text fits. Each field gets the greatest length that works.
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
