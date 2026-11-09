package weburl

import (
	"strings"
)

// Param är ett namn och ett värde ur en fråga.
type Param struct{ Name, Value string }

// ParseQuery tolkar en fråga som URLSearchParams (application/x-www-form-urlencoded): `+` är
// blanksteg, %XX avkodas och ogiltig UTF-8 blir ersättningstecken.
func ParseQuery(query string) []Param {
	var out []Param
	for _, seq := range strings.Split(query, "&") {
		if seq == "" {
			continue
		}
		name, value, _ := strings.Cut(seq, "=")
		out = append(out, Param{decodeForm(name), decodeForm(value)})
	}
	return out
}

func decodeForm(s string) string {
	return DecodeUTF8(PercentDecode(strings.ReplaceAll(s, "+", " ")))
}

// EncodeQuery skriver parametrarna som URLSearchParams gör.
func EncodeQuery(params []Param) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		encodeForm(&b, p.Name)
		b.WriteByte('=')
		encodeForm(&b, p.Value)
	}
	return b.String()
}

func encodeForm(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b.WriteByte('+')
		case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '*' || c == '-' || c == '.' || c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&15])
		}
	}
}

// DecodeUTF8 avkodar UTF-8 som WHATWG Encoding Standard: varje ogiltig del blir ett
// ersättningstecken, och en avbruten sekvens räknas som en del (inte en per byte).
func DecodeUTF8(b []byte) string {
	var out strings.Builder
	out.Grow(len(b))
	needed, seen := 0, 0
	var cp rune
	lower, upper := byte(0x80), byte(0xBF)
	for i := 0; i < len(b); i++ {
		c := b[i]
		if needed == 0 {
			switch {
			case c <= 0x7F:
				out.WriteByte(c)
			case c >= 0xC2 && c <= 0xDF:
				needed, cp = 1, rune(c&0x1F)
			case c >= 0xE0 && c <= 0xEF:
				if c == 0xE0 {
					lower = 0xA0
				}
				if c == 0xED {
					upper = 0x9F
				}
				needed, cp = 2, rune(c&0xF)
			case c >= 0xF0 && c <= 0xF4:
				if c == 0xF0 {
					lower = 0x90
				}
				if c == 0xF4 {
					upper = 0x8F
				}
				needed, cp = 3, rune(c&0x7)
			default:
				out.WriteRune('\uFFFD')
			}
			continue
		}
		if c < lower || c > upper {
			needed, seen, cp = 0, 0, 0
			lower, upper = 0x80, 0xBF
			out.WriteRune('\uFFFD')
			i-- // byten läses om
			continue
		}
		lower, upper = 0x80, 0xBF
		cp = cp<<6 | rune(c&0x3F)
		seen++
		if seen == needed {
			out.WriteRune(cp)
			needed, seen, cp = 0, 0, 0
		}
	}
	if needed != 0 {
		out.WriteRune('\uFFFD')
	}
	return out.String()
}
