//go:build ignore

// Skapar tables.go ur Unicodes teckendatabas, i samma version som Node:s ICU (TypeScript-versionen
// räknade grafemkluster med Intl.Segmenter). Hämta filerna först, till exempel för 17.0.0:
//
//	base=https://www.unicode.org/Public/17.0.0/ucd
//	curl -O $base/auxiliary/GraphemeBreakProperty.txt -O $base/emoji/emoji-data.txt -O $base/DerivedCoreProperties.txt
//	go run gen_tables.go <katalogen med filerna> 17.0.0
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Samma värden som i grapheme.go.
var gcb = map[string]byte{
	"CR": 1, "LF": 2, "Control": 3, "Extend": 4, "ZWJ": 5, "Regional_Indicator": 6, "Prepend": 7,
	"SpacingMark": 8, "L": 9, "V": 10, "T": 11, "LV": 12, "LVT": 13,
}

const (
	extPict       = 1 << 4
	incbLinker    = 1 << 5
	incbConsonant = 2 << 5
	incbExtend    = 3 << 5
)

func main() {
	dir, version := os.Args[1], os.Args[2]
	props := make([]byte, 0x110000)
	read(filepath.Join(dir, "GraphemeBreakProperty.txt"), func(lo, hi rune, fields []string) {
		for r := lo; r <= hi; r++ {
			props[r] |= gcb[fields[0]]
		}
	})
	read(filepath.Join(dir, "emoji-data.txt"), func(lo, hi rune, fields []string) {
		if fields[0] == "Extended_Pictographic" {
			for r := lo; r <= hi; r++ {
				props[r] |= extPict
			}
		}
	})
	read(filepath.Join(dir, "DerivedCoreProperties.txt"), func(lo, hi rune, fields []string) {
		if fields[0] != "InCB" {
			return
		}
		v := map[string]byte{"Linker": incbLinker, "Consonant": incbConsonant, "Extend": incbExtend}[fields[1]]
		for r := lo; r <= hi; r++ {
			props[r] |= v
		}
	})

	var b bytes.Buffer
	fmt.Fprintf(&b, "// Kod genererad av gen_tables.go ur Unicode %s. Ändra inte för hand.\n\npackage format\n\n", version)
	fmt.Fprintf(&b, "const unicodeVersion = %q\n\n", version)
	b.WriteString("// propRanges är sorterade intervall med egenskaperna för varje tecken som har några.\n")
	b.WriteString("var propRanges = []propRange{\n")
	for r := 0; r < len(props); {
		p := props[r]
		end := r
		for end+1 < len(props) && props[end+1] == p {
			end++
		}
		if p != 0 {
			fmt.Fprintf(&b, "{0x%X, 0x%X, 0x%02X},\n", r, end, p)
		}
		r = end + 1
	}
	b.WriteString("}\n")
	src, err := format.Source(b.Bytes())
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("tables.go", src, 0o644); err != nil {
		panic(err)
	}
}

// read anropar fn för varje rad "XXXX..YYYY ; Värde [; Värde] # kommentar".
func read(path string, fn func(lo, hi rune, fields []string)) {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		parts := strings.Split(line, ";")
		if len(parts) < 2 {
			continue
		}
		cps := strings.TrimSpace(parts[0])
		lo, hi, ok := strings.Cut(cps, "..")
		if !ok {
			hi = lo
		}
		l, _ := strconv.ParseUint(lo, 16, 32)
		h, _ := strconv.ParseUint(hi, 16, 32)
		var fields []string
		for _, p := range parts[1:] {
			fields = append(fields, strings.TrimSpace(p))
		}
		fn(rune(l), rune(h), fields)
	}
}
