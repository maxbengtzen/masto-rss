// Package js återskapar de delar av JavaScripts semantik som TypeScript-versionen förlitade sig
// på, så att Go-versionen ger samma text: trim(), \s i reguljära uttryck, String(number),
// encodeURIComponent, slice() på UTF-16-enheter och Date.parse.
package js

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// IsSpace är JavaScripts blanktecken: WhiteSpace och LineTerminator, samma som \s och trim().
func IsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// Trim är String.prototype.trim.
func Trim(s string) string { return strings.TrimFunc(s, IsSpace) }

// TrimEnd är String.prototype.trimEnd.
func TrimEnd(s string) string { return strings.TrimRightFunc(s, IsSpace) }

// CollapseSpace är s.replace(/\s+/g, " ").
func CollapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
			}
			inSpace = true
			continue
		}
		inSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// Slice16 är s.slice(start, end) räknat i UTF-16-enheter, som i JavaScript. Ett surrogatpar som
// delas blir ett ersättningstecken (JavaScript skulle behålla en ensam surrogat, som inte går att
// skriva i UTF-8).
func Slice16(s string, start, end int) string {
	if start <= 0 && utf8.RuneCountInString(s) <= end/2 {
		return s // snabbväg: får plats oavsett
	}
	units := utf16.Encode([]rune(s))
	if end > len(units) {
		end = len(units)
	}
	if start > end {
		return ""
	}
	return string(utf16.Decode(units[start:end]))
}

// FormatNumber är String(n) för ett tal.
func FormatNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		// Exponentform som i JavaScript: 1.5e+21, 1e-7.
		s := strconv.FormatFloat(f, 'e', -1, 64)
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		exp = strings.TrimLeft(exp[1:], "0")
		return mant + "e" + string(sign) + exp
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// EncodeURIComponent är JavaScripts encodeURIComponent.
func EncodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// Less jämför två strängar som JavaScripts < gör: på UTF-16-enheter, inte på bytes.
func Less(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}
