package feed

import (
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"

	"github.com/maxbengtzen/masto-rss/internal/js"
)

// An XML tree built the way the TypeScript version's parser (feedsmith on top of fast-xml-parser)
// saw the document. encoding/xml splits the text into tags, text, comments and CDATA, but the rest
// is done here to come out the same:
//
//   - Names are lowercased, and prefixes for known namespaces are replaced with the canonical ones (a
//     feed that calls Dublin Core "d:" still gets "dc:creator"). The format's own namespace is removed.
//   - Every end tag closes the most recently opened element, whatever its name.
//   - Text and attribute values are stored as they are, with entities. They are decoded only when a
//     field is read.
//   - Fields that feedsmith reads as raw text ("stop nodes", for example title and description)
//     are not parsed at all: the content up to the end tag is stored as is, even broken HTML.

// xnode is an element.
type xnode struct {
	name     string
	attrs    map[string]string // name (with "@") → raw value
	children []*xnode
	// text is the element's text: for a stop node the whole content as is, otherwise the text
	// pieces (CDATA without markers) joined together.
	text string
}

// isObject reports whether the element became an object in feedsmith (has attributes or children),
// and not just a string.
func (n *xnode) isObject() bool { return n != nil && (len(n.attrs) > 0 || len(n.children) > 0) }

// first returns the first child with the name, like parseSingular in feedsmith.
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

// all returns all children with the name.
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

// rawText is retrieveText in feedsmith: the element's text. An object without text has none.
func (n *xnode) rawText() (string, bool) {
	if n == nil {
		return "", false
	}
	if n.isObject() && n.text == "" {
		return "", false
	}
	return n.text, true
}

// str is parseString(retrieveText(n)): the text decoded and trimmed, or empty.
func (n *xnode) str() string {
	text, ok := n.rawText()
	if !ok {
		return ""
	}
	return parseString(text)
}

// attr is parseString of an attribute.
func (n *xnode) attr(name string) string {
	if n == nil {
		return ""
	}
	return parseString(n.attrs["@"+name])
}

// MalformedError and the other errors are named as in feedsmith, since the name shows in the log.
type feedError struct{ name, msg, cause string }

func (e *feedError) Error() string { return e.msg }
func (e *feedError) Name() string  { return e.name }

// CauseMessage is the message from the underlying error, shown in parentheses in the log.
func (e *feedError) CauseMessage() string { return e.cause }

var (
	errUnrecognized = &feedError{name: "DetectError", msg: "Unrecognized feed format"}
	errInvalid      = &feedError{name: "ParseError", msg: "Invalid feed format"}
)

func malformed(cause string) error {
	return &feedError{name: "MalformedError", msg: "Invalid feed format", cause: cause}
}

// nsResolver is feedsmith's createNamespaceResolver: which canonical prefix each prefix maps to.
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
	// The declarations at the start of the document are read in advance, as in feedsmith.
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

// rootIndex is where the root element starts: the first "<" that isn't <? or <!.
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

// declare handles an xmlns attribute.
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

// attrRE is fast-xml-parser's pattern for attributes.
var attrRE = regexp.MustCompile(`([^\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}=]+)[\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(=[\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}]*(?:"([^"]*)"|'([^']*)'))?`)

// splitTag splits a tag (the text between < and >) into name and attributes, like readTagExp.
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

// sanitize replaces characters that encoding/xml refuses to read with spaces, keeping the same
// length in bytes, so that the positions match the original.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == 0xFFFE || r == 0xFFFF:
			return 0xFFFD // same length in UTF-8
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			return ' '
		}
		return r
	}, s)
}

// buildTree parses the document. stopPaths are the format's stop nodes (in addition to the ones
// that apply everywhere).
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
					continue // attributes without a value don't count
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
					// encoding/xml emits an end token for <a/> too; it must not close the parent.
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
	// The text is already decoded to UTF-8 (decodeBody), whatever the XML declaration says.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return d
}

// readStopNode reads the content of a stop node up to its end tag, like readStopNodeData in
// fast-xml-parser. It returns the content and the position after the end tag.
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

// tagExpression reads a tag up to a ">" outside quotes.
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
