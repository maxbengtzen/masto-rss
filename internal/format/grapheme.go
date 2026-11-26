package format

import (
	"sort"
	"unicode/utf8"
)

// Grapheme clusters per UAX #29 (what you see as one character), like Intl.Segmenter in the
// TypeScript version. The tables in tables.go come from the same Unicode version as Node's ICU.

// Properties per character: Grapheme_Cluster_Break in the four lowest bits, Extended_Pictographic
// and Indic_Conjunct_Break in the others.
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
		// ASCII: only control characters, CR and LF have properties.
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

// nextBoundary returns the length in bytes of the first grapheme cluster in s.
func nextBoundary(s string) int {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return 0
	}
	prev := props(r)
	prevGCB := prev & 0x0F
	// State for GB11 (emoji with ZWJ), GB12/13 (flags) and GB9c (Indic conjuncts).
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
		// Update the state after the character.
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

// graphemes splits the text into grapheme clusters.
func graphemes(s string) []string {
	var out []string
	for len(s) > 0 {
		n := nextBoundary(s)
		out = append(out, s[:n])
		s = s[n:]
	}
	return out
}

// graphemeCount counts grapheme clusters without storing them.
func graphemeCount(s string) int {
	n := 0
	for len(s) > 0 {
		s = s[nextBoundary(s):]
		n++
	}
	return n
}
