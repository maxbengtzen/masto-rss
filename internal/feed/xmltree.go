package feed

import (
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/maxbengtzen/masto-rss/internal/js"
)

// Ett XML-träd byggt så som TypeScript-versionens tolk (feedsmith ovanpå fast-xml-parser) såg
// dokumentet. encoding/xml delar upp texten i taggar, text, kommentarer och CDATA, men resten görs
// här för att bli likadant:
//
//   - Namn görs till gemener, och prefix för kända namnrymder byts mot de kanoniska (ett flöde som
//     kallar Dublin Core för "d:" får ändå "dc:creator"). Formatets egen namnrymd tas bort.
//   - Varje sluttagg stänger det senast öppnade elementet, oavsett namn.
//   - Text och attributvärden sparas som de står, med entiteter. De avkodas först när ett fält läses.
//   - Fält som feedsmith läser som rå text ("stoppnoder", till exempel title och description)
//     tolkas inte alls: innehållet fram till sluttaggen sparas som det står, även trasig HTML.

// xnode är ett element.
type xnode struct {
	name     string
	attrs    map[string]string // namn (med "@") → rått värde
	children []*xnode
	// text är elementets text: för en stoppnod hela innehållet som det står, annars textbitarna
	// (CDATA utan markörer) ihopslagna.
	text string
}

// isObject är sant när elementet blev ett objekt i feedsmith (har attribut eller barn), och inte
// bara en sträng.
func (n *xnode) isObject() bool { return n != nil && (len(n.attrs) > 0 || len(n.children) > 0) }

// first är det första barnet med namnet, som parseSingular i feedsmith.
func (n *xnode) first(name string) *xnode {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// all är alla barn med namnet.
func (n *xnode) all(name string) []*xnode {
	if n == nil {
		return nil
	}
	var out []*xnode
	for _, c := range n.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// rawText är retrieveText i feedsmith: elementets text. Ett objekt utan text har ingen.
func (n *xnode) rawText() (string, bool) {
	if n == nil {
		return "", false
	}
	if n.isObject() && n.text == "" {
		return "", false
	}
	return n.text, true
}

// str är parseString(retrieveText(n)): texten avkodad och trimmad, eller tom.
func (n *xnode) str() string {
	text, ok := n.rawText()
	if !ok {
		return ""
	}
	return parseString(text)
}

// attr är parseString av ett attribut.
func (n *xnode) attr(name string) string {
	if n == nil {
		return ""
	}
	return parseString(n.attrs["@"+name])
}

// MalformedError och de andra felen heter som i feedsmith, eftersom namnet syns i loggen.
type feedError struct{ name, msg, cause string }

func (e *feedError) Error() string { return e.msg }
func (e *feedError) Name() string  { return e.name }

// CauseMessage är meddelandet från felet bakom, som visas inom parentes i loggen.
func (e *feedError) CauseMessage() string { return e.cause }

var (
	errUnrecognized = &feedError{name: "DetectError", msg: "Unrecognized feed format"}
	errInvalid      = &feedError{name: "ParseError", msg: "Invalid feed format"}
)

func malformed(cause string) error {
	return &feedError{name: "MalformedError", msg: "Invalid feed format", cause: cause}
}

// nsResolver är feedsmiths createNamespaceResolver: vilket kanoniskt prefix varje prefix har.
type nsResolver struct {
	primary          map[string]bool
	prefixMap        map[string]string
	seeded           map[string]bool
	defaultCanonical *string
	remapping        bool
	tagsSeen         int
}

var xmlnsDeclRE = regexp.MustCompile(`^([\w.-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)

func newResolver(doc string, primary []string) *nsResolver {
	r := &nsResolver{primary: map[string]bool{}, prefixMap: map[string]string{}, seeded: map[string]bool{}}
	for uri, prefix := range namespacePrefixes {
		for _, p := range primary {
			if prefix == p {
				r.primary[uri] = true
			}
		}
	}
	// Deklarationerna i början av dokumentet läses i förväg, som i feedsmith.
	head := doc
	if len(head) > 65536 {
		head = js.Slice16(head, 0, 65536)
	}
	i := strings.Index(head[max(rootIndex(head), 0):], "xmlns:")
	if i >= 0 {
		i += max(rootIndex(head), 0)
	}
	for i >= 0 {
		next := i + 6
		if m := xmlnsDeclRE.FindStringSubmatch(head[i+6:]); m != nil {
			prefix := strings.ToLower(m[1])
			uri := m[2]
			if m[2] == "" && m[3] != "" {
				uri = m[3]
			}
			r.record(prefix, uri)
			r.seeded[prefix] = true
			next = i + 6 + len(m[0])
		}
		j := strings.Index(head[next:], "xmlns:")
		if j < 0 {
			break
		}
		i = next + j
	}
	return r
}

// rootIndex är var rotelementet börjar: första "<" som inte är <? eller <!.
func rootIndex(doc string) int {
	i := strings.IndexByte(doc, '<')
	for i >= 0 {
		if i+1 >= len(doc) || doc[i+1] != '?' && doc[i+1] != '!' {
			return i
		}
		var end int
		if strings.HasPrefix(doc[i:], "<!--") {
			end = strings.Index(doc[i:], "-->")
		} else {
			end = strings.IndexByte(doc[i:], '>')
		}
		if end < 0 {
			return -1
		}
		j := strings.IndexByte(doc[i+end:], '<')
		if j < 0 {
			return -1
		}
		i += end + j
	}
	return -1
}

func (r *nsResolver) resolve(uri string) (string, bool) {
	n := strings.ToLower(js.Trim(uri))
	if r.primary[n] {
		return "", true
	}
	p, ok := namespacePrefixes[n]
	return p, ok
}

func (r *nsResolver) record(prefix, uri string) {
	if _, ok := r.prefixMap[prefix]; ok {
		return
	}
	if canonical, ok := r.resolve(uri); ok {
		r.prefixMap[prefix] = canonical
		if canonical != prefix {
			r.remapping = true
		}
	}
}

// declare tar hand om ett xmlns-attribut.
func (r *nsResolver) declare(rawName, value string) {
	if rawName == "" || rawName[0] != 'x' && rawName[0] != 'X' {
		return
	}
	name := strings.ToLower(rawName)
	if name == "xmlns" {
		if r.tagsSeen == 1 && r.defaultCanonical == nil {
			c, _ := r.resolve(value)
			r.defaultCanonical = &c
			if c != "" {
				r.remapping = true
			}
		}
		return
	}
	if prefix, ok := strings.CutPrefix(name, "xmlns:"); ok {
		if r.seeded[prefix] {
			delete(r.seeded, prefix)
			delete(r.prefixMap, prefix)
		}
		r.record(prefix, value)
	}
}

func (r *nsResolver) transform(name string, useDefault bool) string {
	lowered := strings.ToLower(name)
	if !r.remapping {
		return lowered
	}
	prefix, local, ok := strings.Cut(lowered, ":")
	if !ok {
		if useDefault && r.defaultCanonical != nil && *r.defaultCanonical != "" {
			return *r.defaultCanonical + ":" + lowered
		}
		return lowered
	}
	if prefix == "xmlns" || prefix == "xml" {
		return lowered
	}
	canonical, ok := r.prefixMap[prefix]
	if !ok {
		return lowered
	}
	if canonical == "" {
		return local
	}
	return canonical + ":" + local
}

// attrRE är fast-xml-parsers mönster för attribut.
var attrRE = regexp.MustCompile(`([^\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}=]+)[\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(=[\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(?:"([^"]*)"|'([^']*)'))?`)

// splitTag delar en tagg (texten mellan < och >) i namn och attribut, som readTagExp.
func splitTag(exp string) (name, attrs string, selfClosing bool) {
	exp = strings.ReplaceAll(exp, "\t", " ")
	name = exp
	if i := strings.IndexFunc(exp, js.IsSpace); i >= 0 {
		name = exp[:i]
		attrs = strings.TrimLeftFunc(exp[i+1:], js.IsSpace)
	} else {
		attrs = exp
	}
	if strings.HasSuffix(attrs, "/") {
		selfClosing = true
		if strings.HasSuffix(name, "/") {
			name = name[:len(name)-1]
			attrs = name
		} else {
			attrs = attrs[:len(attrs)-1]
		}
	}
	if attrs == name {
		attrs = ""
	}
	return name, attrs, selfClosing
}

// sanitize byter tecken som encoding/xml vägrar läsa mot blanksteg, med samma längd i bytes, så
// att positionerna stämmer mot originalet.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0xFFFE || r == 0xFFFF:
			return 0xFFFD // samma längd i UTF-8
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			return ' '
		}
		return r
	}, s)
}

// buildTree tolkar dokumentet. stopPaths är formatets stoppnoder (utöver de som gäller överallt).
func buildTree(doc string, primary []string, stopPaths map[string]bool) (*xnode, error) {
	src := strings.ReplaceAll(strings.ReplaceAll(doc, "\r\n", "\n"), "\r", "\n")
	clean := sanitize(src)
	res := newResolver(src, primary)
	root := &xnode{}
	stack := []*xnode{root}
	var path []string

	base := 0
	dec := newDecoder(clean, base)
	for {
		before := base + int(dec.InputOffset())
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, malformed(err.Error())
		}
		after := base + int(dec.InputOffset())
		top := stack[len(stack)-1]
		switch tok.(type) {
		case xml.StartElement:
			rawName, attrStr, selfClosing := splitTag(src[before+1 : after-1])
			res.tagsSeen++
			attrs := map[string]string{}
			for _, m := range attrRE.FindAllStringSubmatchIndex(attrStr, -1) {
				if m[4] < 0 {
					continue // attribut utan värde räknas inte
				}
				name := attrStr[m[2]:m[3]]
				value := ""
				if m[6] >= 0 {
					value = attrStr[m[6]:m[7]]
				} else if m[8] >= 0 {
					value = attrStr[m[8]:m[9]]
				}
				res.declare(name, value)
				attrs[name] = value
			}
			n := &xnode{name: res.transform(rawName, true), attrs: map[string]string{}}
			for name, value := range attrs {
				n.attrs["@"+res.transform(name, false)] = value
			}
			top.children = append(top.children, n)
			p := append(path, n.name)
			if stopAnywhere[n.name] || stopPaths[strings.Join(p, ".")] {
				if !selfClosing {
					content, end, ok := readStopNode(src, rawName, after)
					if !ok {
						return nil, malformed("Unexpected end of " + rawName)
					}
					n.text = content
					base = end
					dec = newDecoder(clean[base:], base)
				} else {
					// encoding/xml ger ett sluttecken för <a/> också; det ska inte stänga föräldern.
					dec.RawToken()
				}
				continue
			}
			if selfClosing {
				dec.RawToken()
				continue
			}
			stack = append(stack, n)
			path = p
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
				path = path[:len(path)-1]
			}
		case xml.CharData:
			raw := src[before:after]
			if strings.HasPrefix(raw, "<![CDATA[") {
				raw = strings.TrimSuffix(raw[9:], "]]>")
			}
			if top != root {
				top.text += raw
			}
		}
	}
	return root, nil
}

func newDecoder(s string, base int) *xml.Decoder {
	d := xml.NewDecoder(strings.NewReader(s))
	d.Strict = false
	// Texten är redan avkodad till UTF-8 (decodeBody), vad XML-deklarationen än säger.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return d
}

// readStopNode läser innehållet i en stoppnod fram till dess sluttagg, som readStopNodeData i
// fast-xml-parser. Den returnerar innehållet och positionen efter sluttaggen.
func readStopNode(src, tagName string, i int) (string, int, bool) {
	start, open := i, 1
	for ; i < len(src); i++ {
		if src[i] != '<' {
			continue
		}
		rest := src[i:]
		switch {
		case strings.HasPrefix(rest, "</"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				return "", 0, false
			}
			if js.Trim(rest[2:end]) == tagName {
				open--
				if open == 0 {
					return src[start:i], i + end + 1, true
				}
			}
			i += end
		case strings.HasPrefix(rest, "<?"):
			end := strings.Index(rest[1:], "?>")
			if end < 0 {
				return "", 0, false
			}
			i += 1 + end + 1
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest[3:], "-->")
			if end < 0 {
				return "", 0, false
			}
			i += 3 + end + 2
		case strings.HasPrefix(rest, "<!["):
			end := strings.Index(rest, "]]>")
			if end < 0 {
				return "", 0, false
			}
			i += end
		default:
			exp, end, ok := tagExpression(src, i+1)
			if ok {
				name, _, _ := splitTagName(exp)
				if name == tagName && !strings.HasSuffix(exp, "/") {
					open++
				}
				i = end
			}
		}
	}
	return "", 0, false
}

// tagExpression läser en tagg fram till ">" utanför citattecken.
func tagExpression(src string, i int) (string, int, bool) {
	var quote byte
	for j := i; j < len(src); j++ {
		c := src[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return src[i:j], j, true
		}
	}
	return "", 0, false
}

func splitTagName(exp string) (name, rest string, ok bool) {
	exp = strings.ReplaceAll(exp, "\t", " ")
	if i := strings.IndexFunc(exp, js.IsSpace); i >= 0 {
		return exp[:i], exp[i+1:], true
	}
	return exp, "", true
}
