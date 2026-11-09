package format

import (
	"sort"
	"unicode/utf8"
)

// Grafemkluster enligt UAX #29 (det man ser som ett tecken), som Intl.Segmenter i
// TypeScript-versionen. Tabellerna i tables.go kommer ur samma Unicode-version som Node:s ICU.

// Egenskaper per tecken: Grapheme_Cluster_Break i de fyra lägsta bitarna, Extended_Pictographic
// och Indic_Conjunct_Break i de övriga.
const (
	gcbOther byte = iota
	gcbCR
	gcbLF
	gcbControl
	gcbExtend
	gcbZWJ
	gcbRI
	gcbPrepend
	gcbSpacingMark
	gcbL
	gcbV
	gcbT
	gcbLV
	gcbLVT
)

const (
	propExtPict   = 1 << 4
	incbMask      = 3 << 5
	incbLinker    = 1 << 5
	incbConsonant = 2 << 5
	incbExtend    = 3 << 5
)

type propRange struct {
	lo, hi rune
	props  byte
}

func props(r rune) byte {
	if r < 0x80 {
		// ASCII: bara styrtecken, CR och LF har egenskaper.
		switch {
		case r == '\r':
			return gcbCR
		case r == '\n':
			return gcbLF
		case r < 0x20 || r == 0x7F:
			return gcbControl
		}
		return 0
	}
	i := sort.Search(len(propRanges), func(i int) bool { return propRanges[i].hi >= r })
	if i < len(propRanges) && propRanges[i].lo <= r {
		return propRanges[i].props
	}
	return 0
}

// nextBoundary ger längden i bytes av första grafemklustret i s.
func nextBoundary(s string) int {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return 0
	}
	prev := props(r)
	prevGCB := prev & 0x0F
	// Tillstånd för GB11 (emoji med ZWJ), GB12/13 (flaggor) och GB9c (indiska konjunkter).
	emoji := prev&propExtPict != 0
	emojiZWJ := false
	ri := 0
	if prevGCB == gcbRI {
		ri = 1
	}
	conjunct := prev&incbMask == incbConsonant
	linker := false

	i := size
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		p := props(r)
		gcb := p & 0x0F
		incb := p & incbMask
		if isBoundary(prevGCB, gcb, p, emojiZWJ, ri, conjunct && linker) {
			return i
		}
		// Uppdatera tillståndet efter tecknet.
		switch {
		case p&propExtPict != 0:
			emoji, emojiZWJ = true, false
		case gcb == gcbExtend && emoji && !emojiZWJ:
		case gcb == gcbZWJ && emoji && !emojiZWJ:
			emojiZWJ = true
		default:
			emoji, emojiZWJ = false, false
		}
		if gcb == gcbRI {
			ri++
		} else {
			ri = 0
		}
		switch {
		case incb == incbConsonant:
			conjunct, linker = true, false
		case conjunct && incb == incbLinker:
			linker = true
		case conjunct && incb == incbExtend:
		default:
			conjunct, linker = false, false
		}
		prevGCB = gcb
		i += size
	}
	return i
}

func isBoundary(prev, next, nextProps byte, emojiZWJ bool, ri int, conjunctLinker bool) bool {
	switch {
	case prev == gcbCR && next == gcbLF: // GB3
		return false
	case prev == gcbControl || prev == gcbCR || prev == gcbLF: // GB4
		return true
	case next == gcbControl || next == gcbCR || next == gcbLF: // GB5
		return true
	case prev == gcbL && (next == gcbL || next == gcbV || next == gcbLV || next == gcbLVT): // GB6
		return false
	case (prev == gcbLV || prev == gcbV) && (next == gcbV || next == gcbT): // GB7
		return false
	case (prev == gcbLVT || prev == gcbT) && next == gcbT: // GB8
		return false
	case next == gcbExtend || next == gcbZWJ: // GB9
		return false
	case next == gcbSpacingMark: // GB9a
		return false
	case prev == gcbPrepend: // GB9b
		return false
	case conjunctLinker && nextProps&incbMask == incbConsonant: // GB9c
		return false
	case emojiZWJ && prev == gcbZWJ && nextProps&propExtPict != 0: // GB11
		return false
	case prev == gcbRI && next == gcbRI && ri%2 == 1: // GB12, GB13
		return false
	}
	return true // GB999
}

// graphemes delar texten i grafemkluster.
func graphemes(s string) []string {
	var out []string
	for len(s) > 0 {
		n := nextBoundary(s)
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

// graphemeCount räknar grafemkluster utan att spara dem.
func graphemeCount(s string) int {
	n := 0
	for len(s) > 0 {
		s = s[nextBoundary(s):]
		n++
	}
	return n
}
