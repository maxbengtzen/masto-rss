package feed

import (
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

var (
	charsetRE = regexp.MustCompile(`(?i)charset[` + jsSpace + `]*=[` + jsSpace + `]*"?([\w.:-]+)`)
	xmlEncRE  = regexp.MustCompile(`^[` + jsSpace + `]*<\?(?i:xml)[^>]*\b(?i:encoding)[` + jsSpace + `]*=[` + jsSpace + `]*["']([\w.:-]+)["']`)
)

// DecodeBody avkodar svaret: BOM först, sedan Content-Type, sedan XML-deklarationen, annars UTF-8.
//
// Teckenkodningarna är de vanliga i flöden: UTF-8, UTF-16 och windows-1252 (som också är vad
// ISO-8859-1, latin1 och ascii betyder i webbläsare och i TextDecoder), samt ISO-8859-15. Andra
// avkodas som UTF-8, precis som en okänd teckenkodning gjorde i TypeScript-versionen.
func DecodeBody(b []byte, contentType string) string {
	var label string
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		label = "utf-8"
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		label = "utf-16le"
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		label = "utf-16be"
	default:
		if m := charsetRE.FindStringSubmatch(contentType); m != nil {
			label = m[1]
		} else {
			head := b
			if len(head) > 512 {
				head = head[:512]
			}
			if m := xmlEncRE.FindStringSubmatch(decodeSingleByte(head, &windows1252)); m != nil {
				label = m[1]
			} else {
				label = "utf-8"
			}
		}
	}
	switch encodingFor(label) {
	case "utf-16le":
		return decodeUTF16(b, false)
	case "utf-16be":
		return decodeUTF16(b, true)
	case "windows-1252":
		return decodeSingleByte(b, &windows1252)
	case "iso-8859-15":
		return decodeSingleByte(b, &iso885915)
	}
	// TextDecoder tar bort en inledande BOM.
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	return weburl.DecodeUTF8(b)
}

// encodingFor ger teckenkodningen för en etikett enligt WHATWG Encoding Standard.
func encodingFor(label string) string {
	switch strings.ToLower(js.Trim(label)) {
	case "unicode-1-1-utf-8", "unicode11utf8", "unicode20utf8", "utf-8", "utf8", "x-unicode20utf8":
		return "utf-8"
	case "csunicode", "iso-10646-ucs-2", "ucs-2", "unicode", "unicodefeff", "utf-16", "utf-16le":
		return "utf-16le"
	case "unicodefffe", "utf-16be":
		return "utf-16be"
	case "ansi_x3.4-1968", "ascii", "cp1252", "cp819", "csisolatin1", "ibm819", "iso-8859-1", "iso-ir-100",
		"iso8859-1", "iso88591", "iso_8859-1", "iso_8859-1:1987", "l1", "latin1", "us-ascii", "windows-1252", "x-cp1252":
		return "windows-1252"
	case "csisolatin9", "iso-8859-15", "iso8859-15", "iso885915", "iso_8859-15", "l9":
		return "iso-8859-15"
	}
	return "utf-8"
}

func decodeUTF16(b []byte, bigEndian bool) string {
	// TextDecoder tar bort en BOM som stämmer med ordningen.
	if len(b) >= 2 && (bigEndian && b[0] == 0xFE && b[1] == 0xFF || !bigEndian && b[0] == 0xFF && b[1] == 0xFE) {
		b = b[2:]
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	s := string(utf16.Decode(units))
	if len(b)%2 == 1 {
		s += "�"
	}
	return s
}

func decodeSingleByte(b []byte, high *[128]rune) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c < 0x80 {
			sb.WriteByte(c)
		} else {
			sb.WriteRune(high[c-0x80])
		}
	}
	return sb.String()
}

// windows1252 är tecknen för 0x80–0xFF.
var windows1252 = [128]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
	0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7, 0xA8, 0xA9, 0xAA, 0xAB, 0xAC, 0xAD, 0xAE, 0xAF,
	0xB0, 0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB6, 0xB7, 0xB8, 0xB9, 0xBA, 0xBB, 0xBC, 0xBD, 0xBE, 0xBF,
	0xC0, 0xC1, 0xC2, 0xC3, 0xC4, 0xC5, 0xC6, 0xC7, 0xC8, 0xC9, 0xCA, 0xCB, 0xCC, 0xCD, 0xCE, 0xCF,
	0xD0, 0xD1, 0xD2, 0xD3, 0xD4, 0xD5, 0xD6, 0xD7, 0xD8, 0xD9, 0xDA, 0xDB, 0xDC, 0xDD, 0xDE, 0xDF,
	0xE0, 0xE1, 0xE2, 0xE3, 0xE4, 0xE5, 0xE6, 0xE7, 0xE8, 0xE9, 0xEA, 0xEB, 0xEC, 0xED, 0xEE, 0xEF,
	0xF0, 0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7, 0xF8, 0xF9, 0xFA, 0xFB, 0xFC, 0xFD, 0xFE, 0xFF,
}

// iso885915 är ISO-8859-1 med euro och några franska och finska bokstäver.
var iso885915 = func() [128]rune {
	var t [128]rune
	for i := range t {
		t[i] = rune(0x80 + i)
	}
	for c, r := range map[int]rune{0xA4: 0x20AC, 0xA6: 0x0160, 0xA8: 0x0161, 0xB4: 0x017D, 0xB8: 0x017E, 0xBC: 0x0152, 0xBD: 0x0153, 0xBE: 0x0178} {
		t[c-0x80] = r
	}
	return t
}()
