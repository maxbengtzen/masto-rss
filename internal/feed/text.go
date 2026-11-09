package feed

import (
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

// --- Som feedsmith läser ett fält -----------------------------------------------------------

// parseString är feedsmiths parseString: kommentarer bort, CDATA som det står, HTML-entiteter
// utanför CDATA avkodade (bara om det finns ett & med ett ; efter), och blanktecken runt om bort.
func parseString(s string) string {
	if s == "" {
		return ""
	}
	return js.Trim(decodeWithCdata(stripComments(s)))
}

func hasEntities(s string) bool {
	amp := strings.IndexByte(s, '&')
	return amp >= 0 && strings.IndexByte(s[amp:], ';') >= 0
}

func decodeHTML(s string) string {
	if !hasEntities(s) {
		return s
	}
	return html.UnescapeString(s)
}

func stripComments(s string) string {
	i := strings.Index(s, "<!--")
	if i < 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for i >= 0 {
		b.WriteString(s[last:i])
		end := strings.Index(s[i+4:], "-->")
		if end < 0 {
			return s // en kommentar som aldrig slutar: allt lämnas som det står
		}
		last = i + 4 + end + 3
		j := strings.Index(s[last:], "<!--")
		if j < 0 {
			break
		}
		i = last + j
	}
	b.WriteString(s[last:])
	return b.String()
}

func decodeWithCdata(s string) string {
	i := strings.Index(s, "<![CDATA[")
	if i < 0 {
		return decodeHTML(s)
	}
	var b strings.Builder
	last := 0
	for i >= 0 {
		b.WriteString(decodeHTML(s[last:i]))
		end := strings.Index(s[i+9:], "]]>")
		if end < 0 {
			return decodeHTML(s)
		}
		b.WriteString(s[i+9 : i+9+end])
		last = i + 9 + end + 3
		j := strings.Index(s[last:], "<![CDATA[")
		if j < 0 {
			break
		}
		i = last + j
	}
	b.WriteString(decodeHTML(s[last:]))
	return b.String()
}

// --- Text och länkar -----------------------------------------------------------------------

var entities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'", "nbsp": "\u00A0", "ensp": "\u2002", "emsp": "\u2003",
	"thinsp": "\u2009", "shy": "", "hellip": "…", "ndash": "–", "mdash": "—", "lsquo": "‘", "rsquo": "’", "sbquo": "‚",
	"ldquo": "“", "rdquo": "”", "bdquo": "„", "laquo": "«", "raquo": "»", "lsaquo": "‹", "rsaquo": "›", "bull": "•",
	"middot": "·", "copy": "©", "reg": "®", "trade": "™", "euro": "€", "pound": "£", "deg": "°", "times": "×",
	"divide": "÷", "plusmn": "±", "frac12": "½", "frac14": "¼", "frac34": "¾", "sect": "§", "para": "¶",
	"aring": "å", "Aring": "Å", "auml": "ä", "Auml": "Ä", "ouml": "ö", "Ouml": "Ö", "aelig": "æ", "AElig": "Æ",
	"oslash": "ø", "Oslash": "Ø", "eacute": "é", "Eacute": "É", "egrave": "è", "Egrave": "È", "ecirc": "ê",
	"aacute": "á", "agrave": "à", "iacute": "í", "oacute": "ó", "uacute": "ú", "uuml": "ü", "Uuml": "Ü",
	"ntilde": "ñ", "ccedil": "ç", "szlig": "ß",
}

var entityRE = regexp.MustCompile(`&(#[xX][0-9a-fA-F]+|#\d+|[a-zA-Z][a-zA-Z0-9]*);`)

// DecodeEntities gör HTML-entiteter till tecken. Okända namngivna entiteter lämnas som de är.
func DecodeEntities(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	out := entityRE.ReplaceAllStringFunc(text, func(whole string) string {
		name := whole[1 : len(whole)-1]
		if name[0] != '#' {
			if v, ok := entities[name]; ok {
				return v
			}
			return whole
		}
		var code float64
		if name[1] == 'x' || name[1] == 'X' {
			code = parseIntJS(name[2:], 16)
		} else {
			code = parseIntJS(name[1:], 10)
		}
		if code > 0 && code <= 0x10FFFF {
			if code >= 0xD800 && code <= 0xDFFF {
				// Surrogater sätts ihop efteråt, som när JavaScript skriver dem i följd.
				return string(rune(surrogateMark + code - 0xD800))
			}
			return string(rune(code))
		}
		return whole
	})
	if !strings.ContainsFunc(out, isSurrogateMark) {
		return out
	}
	runes := []rune(out)
	var b strings.Builder
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if !isSurrogateMark(r) {
			b.WriteRune(r)
			continue
		}
		hi := r - surrogateMark + 0xD800
		if hi < 0xDC00 && i+1 < len(runes) && isSurrogateMark(runes[i+1]) && runes[i+1]-surrogateMark+0xD800 >= 0xDC00 {
			lo := runes[i+1] - surrogateMark + 0xD800
			b.WriteRune((hi-0xD800)<<10 + (lo - 0xDC00) + 0x10000)
			i++
			continue
		}
		b.WriteRune(utf8.RuneError) // en ensam surrogat går inte att skriva i UTF-8
	}
	return b.String()
}

// surrogateMark är var i ett privat plan surrogaterna hålls tills de satts ihop.
const surrogateMark = 0x10F000

func isSurrogateMark(r rune) bool { return r >= surrogateMark && r < surrogateMark+0x800 }

// parseIntJS är parseInt för en sträng med bara siffror: för stora tal blir stora, inte fel.
func parseIntJS(digits string, base int) float64 {
	v, err := strconv.ParseUint(digits, base, 64)
	if err != nil {
		return 1e300
	}
	return float64(v)
}

// blockTag är taggar som skiljer stycken åt och därför blir ett blanksteg.
var blockTag = regexp.MustCompile(ci(`</?(?:p|div|br|hr|li|ul|ol|h[1-6]|blockquote|pre|table|tr|td|th|section|article|figure|figcaption|header|footer)\b[^>]*>`))
var commentRE = regexp.MustCompile(`<!--[\s\S]*?-->`)
var tagRE = regexp.MustCompile(`<[^>]*>`)

// ci gör bokstäverna i ett mönster skiftlägesokänsliga bara för ASCII, som /i i JavaScript (Go:s
// (?i) räknar också till exempel ſ som s).
func ci(pattern string) string {
	var b strings.Builder
	escaped := false
	for _, r := range pattern {
		if escaped || r == '\\' {
			escaped = !escaped && r == '\\'
			b.WriteRune(r)
			continue
		}
		if 'a' <= r && r <= 'z' {
			b.WriteString("[" + string(r) + string(r-32) + "]")
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// HTMLToText gör HTML till en rad text: taggar bort, entiteter avkodade, blanktecken hopslagna.
func HTMLToText(s string) string {
	s = removeScripts(s)
	s = commentRE.ReplaceAllString(s, " ")
	s = blockTag.ReplaceAllString(s, " ")
	s = tagRE.ReplaceAllString(s, "")
	return js.Trim(js.CollapseSpace(DecodeEntities(s)))
}

// removeScripts är s.replace(/<(script|style)\b[\s\S]*?<\/\1\s*>/gi, " ") (Go:s regexp saknar
// bakåtreferenser).
func removeScripts(s string) string {
	lower := asciiLower(s)
	if !strings.Contains(lower, "<script") && !strings.Contains(lower, "<style") {
		return s
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		for _, tag := range []string{"script", "style"} {
			open := i + 1 + len(tag)
			if !strings.HasPrefix(lower[i+1:], tag) || open < len(s) && isWordByte(s[open]) {
				continue
			}
			end := closingTag(lower, open, tag)
			if end < 0 {
				continue
			}
			b.WriteString(s[last:i])
			b.WriteByte(' ')
			last = end
			i = end - 1
			break
		}
	}
	b.WriteString(s[last:])
	return b.String()
}

// closingTag letar efter </tag följt av blanktecken och >, och ger positionen efter >.
func closingTag(lower string, from int, tag string) int {
	for {
		j := strings.Index(lower[from:], "</"+tag)
		if j < 0 {
			return -1
		}
		k := from + j + 2 + len(tag)
		for k < len(lower) {
			r, size := utf8.DecodeRuneInString(lower[k:])
			if !js.IsSpace(r) {
				break
			}
			k += size
		}
		if k < len(lower) && lower[k] == '>' {
			return k + 1
		}
		from += j + 1
	}
}

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func isWordByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
}

// tracking är spårningsparametrar som inte ska påverka om två länkar räknas som samma.
var tracking = regexp.MustCompile(ci(`^(?:utm_\w+|fbclid|gclid|dclid|gbraid|wbraid|msclkid|yclid|mc_cid|mc_eid|_hsenc|_hsmi|igshid)$`))

// NormalizeLink gör länken till en nyckel: utan schema, `www.`, spårningsparametrar, fragment och
// avslutande snedstreck, och med övriga parametrar i bokstavsordning. Två länkar som bara skiljer
// sig så räknas som samma post.
func NormalizeLink(link string) string {
	raw := js.Trim(link)
	u, err := weburl.Parse(raw, nil)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return raw
	}
	var params []weburl.Param
	if u.Query != nil {
		for _, p := range weburl.ParseQuery(*u.Query) {
			if !tracking.MatchString(p.Name) {
				params = append(params, p)
			}
		}
	}
	sort.SliceStable(params, func(i, j int) bool { return js.Less(params[i].Name, params[j].Name) })
	query := ""
	if len(params) > 0 {
		query = "?" + weburl.EncodeQuery(params)
	}
	return strings.TrimPrefix(u.HostPort(), "www.") + strings.TrimRight(u.Pathname(), "/") + query
}
