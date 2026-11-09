// Package weburl tolkar och skriver adresser enligt WHATWG URL Standard, som `new URL()` och
// `URLSearchParams` i JavaScript. TypeScript-versionen använde dem för att göra relativa länkar
// absoluta och för att normalisera länkar till nycklar, och nycklarna i databasen ska bli desamma.
// Go:s net/url följer RFC 3986 och skiljer sig i detaljer (procentkodning, IDN, IPv4 med hex).
package weburl

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// URL är en tolkad adress.
type URL struct {
	Scheme   string
	Username string
	Password string
	// Host är nil när adressen saknar värd (till exempel mailto:).
	Host *string
	// Port är -1 när ingen port anges eller när den är schemats standardport.
	Port int
	// Path är segmenten, eller nil när adressen har en ogenomskinlig sökväg (Opaque).
	Path     []string
	Opaque   *string
	Query    *string
	Fragment *string
}

// ErrInvalid är felet för en adress som inte går att tolka (TypeError: Invalid URL i JavaScript).
var ErrInvalid = errors.New("Invalid URL")

var specialPorts = map[string]int{"ftp": 21, "file": -1, "http": 80, "https": 443, "ws": 80, "wss": 443}

func isSpecial(scheme string) bool { _, ok := specialPorts[scheme]; return ok }

// IsSpecial är sant för http, https, ftp, file, ws och wss.
func (u *URL) IsSpecial() bool { return isSpecial(u.Scheme) }

// Href är adressen som text (url.href).
func (u *URL) Href() string {
	var b strings.Builder
	b.WriteString(u.Scheme)
	b.WriteByte(':')
	if u.Host != nil {
		b.WriteString("//")
		if u.Username != "" || u.Password != "" {
			b.WriteString(u.Username)
			if u.Password != "" {
				b.WriteByte(':')
				b.WriteString(u.Password)
			}
			b.WriteByte('@')
		}
		b.WriteString(*u.Host)
		if u.Port >= 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(u.Port))
		}
	} else if u.Opaque == nil && len(u.Path) > 1 && u.Path[0] == "" {
		b.WriteString("/.")
	}
	b.WriteString(u.Pathname())
	if u.Query != nil {
		b.WriteByte('?')
		b.WriteString(*u.Query)
	}
	if u.Fragment != nil {
		b.WriteByte('#')
		b.WriteString(*u.Fragment)
	}
	return b.String()
}

// Pathname är url.pathname.
func (u *URL) Pathname() string {
	if u.Opaque != nil {
		return *u.Opaque
	}
	var b strings.Builder
	for _, seg := range u.Path {
		b.WriteByte('/')
		b.WriteString(seg)
	}
	return b.String()
}

// HostPort är url.host: värden och porten om den inte är standardporten.
func (u *URL) HostPort() string {
	if u.Host == nil {
		return ""
	}
	if u.Port >= 0 {
		return *u.Host + ":" + strconv.Itoa(u.Port)
	}
	return *u.Host
}

// Search är url.search: "?" och frågan, eller tomt.
func (u *URL) Search() string {
	if u.Query == nil || *u.Query == "" {
		return ""
	}
	return "?" + *u.Query
}

// Origin är url.origin för http(s).
func (u *URL) Origin() string { return u.Scheme + "://" + u.HostPort() }

// Parse är new URL(input) och new URL(input, base).
func Parse(input string, base *URL) (*URL, error) {
	// Inledande och avslutande styrtecken och blanksteg tas bort, och tabbar och radbrytningar
	// var som helst.
	input = strings.TrimFunc(input, func(r rune) bool { return r <= 0x20 })
	input = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, input)
	p := &parser{in: []rune(input), base: base, url: &URL{Port: -1}}
	if err := p.run(); err != nil {
		return nil, err
	}
	return p.url, nil
}

// ParseRef är new URL(input, base) med base som text.
func ParseRef(input, base string) (*URL, error) {
	b, err := Parse(base, nil)
	if err != nil {
		return nil, err
	}
	return Parse(input, b)
}

type state int

const (
	stSchemeStart state = iota
	stScheme
	stNoScheme
	stSpecialRelativeOrAuthority
	stPathOrAuthority
	stRelative
	stRelativeSlash
	stSpecialAuthoritySlashes
	stSpecialAuthorityIgnoreSlashes
	stAuthority
	stHost
	stPort
	stFile
	stFileSlash
	stFileHost
	stPathStart
	stPath
	stOpaquePath
	stQuery
	stFragment
)

type parser struct {
	in   []rune
	base *URL
	url  *URL
}

const eof = -1

func isASCIIAlpha(c rune) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func isASCIIDigit(c rune) bool { return '0' <= c && c <= '9' }
func isASCIIAlnum(c rune) bool { return isASCIIAlpha(c) || isASCIIDigit(c) }
func isASCIIHex(c rune) bool   { return isASCIIDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }
func lowerASCII(c rune) rune {
	if 'A' <= c && c <= 'Z' {
		return c + 32
	}
	return c
}
func ptr(s string) *string { return &s }
func isWindowsDriveLetter(s string, normalized bool) bool {
	if len(s) != 2 || !isASCIIAlpha(rune(s[0])) {
		return false
	}
	return s[1] == ':' || !normalized && s[1] == '|'
}

func (p *parser) at(i int) rune {
	if i < len(p.in) {
		return p.in[i]
	}
	return eof
}

func (p *parser) startsWithDriveLetter(i int) bool {
	if i+1 >= len(p.in) || !isASCIIAlpha(p.in[i]) || p.in[i+1] != ':' && p.in[i+1] != '|' {
		return false
	}
	c := p.at(i + 2)
	return c == eof || c == '/' || c == '\\' || c == '?' || c == '#'
}

func (p *parser) shortenPath() {
	u := p.url
	if u.Scheme == "file" && len(u.Path) == 1 && isWindowsDriveLetter(u.Path[0], true) {
		return
	}
	if len(u.Path) > 0 {
		u.Path = u.Path[:len(u.Path)-1]
	}
}

func copyPath(path []string) []string { return append([]string(nil), path...) }

func (p *parser) run() error {
	u, base := p.url, p.base
	st := stSchemeStart
	var buf strings.Builder
	atSignSeen, insideBrackets, passwordTokenSeen := false, false, false

	for i := 0; ; i++ {
		c := p.at(i)
		switch st {
		case stSchemeStart:
			if isASCIIAlpha(c) {
				buf.WriteRune(lowerASCII(c))
				st = stScheme
			} else {
				st = stNoScheme
				i--
			}
		case stScheme:
			if isASCIIAlnum(c) || c == '+' || c == '-' || c == '.' {
				buf.WriteRune(lowerASCII(c))
			} else if c == ':' {
				u.Scheme = buf.String()
				buf.Reset()
				switch {
				case u.Scheme == "file":
					st = stFile
				case u.IsSpecial() && base != nil && base.Scheme == u.Scheme:
					st = stSpecialRelativeOrAuthority
				case u.IsSpecial():
					st = stSpecialAuthoritySlashes
				case p.at(i+1) == '/':
					st = stPathOrAuthority
					i++
				default:
					u.Opaque = ptr("")
					st = stOpaquePath
				}
			} else {
				buf.Reset()
				st = stNoScheme
				i = -1
			}
		case stNoScheme:
			if base == nil || base.Opaque != nil && c != '#' {
				return ErrInvalid
			}
			if base.Opaque != nil && c == '#' {
				u.Scheme = base.Scheme
				u.Opaque = ptr(*base.Opaque)
				u.Query = copyStr(base.Query)
				u.Fragment = ptr("")
				st = stFragment
			} else if base.Scheme != "file" {
				st = stRelative
				i--
			} else {
				st = stFile
				i--
			}
		case stSpecialRelativeOrAuthority:
			if c == '/' && p.at(i+1) == '/' {
				st = stSpecialAuthorityIgnoreSlashes
				i++
			} else {
				st = stRelative
				i--
			}
		case stPathOrAuthority:
			if c == '/' {
				st = stAuthority
			} else {
				st = stPath
				i--
			}
		case stRelative:
			u.Scheme = base.Scheme
			if c == '/' {
				st = stRelativeSlash
			} else if u.IsSpecial() && c == '\\' {
				st = stRelativeSlash
			} else {
				u.Username, u.Password, u.Host, u.Port = base.Username, base.Password, copyStr(base.Host), base.Port
				u.Path = copyPath(base.Path)
				u.Query = copyStr(base.Query)
				if c == '?' {
					u.Query = ptr("")
					st = stQuery
				} else if c == '#' {
					u.Fragment = ptr("")
					st = stFragment
				} else if c != eof {
					u.Query = nil
					p.shortenPath()
					st = stPath
					i--
				}
			}
		case stRelativeSlash:
			if u.IsSpecial() && (c == '/' || c == '\\') {
				st = stSpecialAuthorityIgnoreSlashes
			} else if c == '/' {
				st = stAuthority
			} else {
				u.Username, u.Password, u.Host, u.Port = base.Username, base.Password, copyStr(base.Host), base.Port
				st = stPath
				i--
			}
		case stSpecialAuthoritySlashes:
			if c == '/' && p.at(i+1) == '/' {
				st = stSpecialAuthorityIgnoreSlashes
				i++
			} else {
				st = stSpecialAuthorityIgnoreSlashes
				i--
			}
		case stSpecialAuthorityIgnoreSlashes:
			if c != '/' && c != '\\' {
				st = stAuthority
				i--
			}
		case stAuthority:
			if c == '@' {
				s := buf.String()
				if atSignSeen {
					s = "%40" + s
				}
				atSignSeen = true
				for _, r := range s {
					if r == ':' && !passwordTokenSeen {
						passwordTokenSeen = true
						continue
					}
					enc := percentEncodeRune(r, inUserinfoSet)
					if passwordTokenSeen {
						u.Password += enc
					} else {
						u.Username += enc
					}
				}
				buf.Reset()
			} else if c == eof || c == '/' || c == '?' || c == '#' || u.IsSpecial() && c == '\\' {
				if atSignSeen && buf.Len() == 0 {
					return ErrInvalid
				}
				i -= utf8.RuneCountInString(buf.String()) + 1
				buf.Reset()
				st = stHost
			} else {
				buf.WriteRune(c)
			}
		case stHost:
			if c == ':' && !insideBrackets {
				if buf.Len() == 0 {
					return ErrInvalid
				}
				host, err := parseHost(buf.String(), !u.IsSpecial())
				if err != nil {
					return err
				}
				u.Host = &host
				buf.Reset()
				st = stPort
			} else if c == eof || c == '/' || c == '?' || c == '#' || u.IsSpecial() && c == '\\' {
				i--
				if u.IsSpecial() && buf.Len() == 0 {
					return ErrInvalid
				}
				host, err := parseHost(buf.String(), !u.IsSpecial())
				if err != nil {
					return err
				}
				u.Host = &host
				buf.Reset()
				st = stPathStart
			} else {
				if c == '[' {
					insideBrackets = true
				}
				if c == ']' {
					insideBrackets = false
				}
				buf.WriteRune(c)
			}
		case stPort:
			if isASCIIDigit(c) {
				buf.WriteRune(c)
			} else if c == eof || c == '/' || c == '?' || c == '#' || u.IsSpecial() && c == '\\' {
				if buf.Len() > 0 {
					port, err := strconv.Atoi(buf.String())
					if err != nil || port > 65535 {
						return ErrInvalid
					}
					if def, ok := specialPorts[u.Scheme]; ok && def == port {
						port = -1
					}
					u.Port = port
					buf.Reset()
				}
				st = stPathStart
				i--
			} else {
				return ErrInvalid
			}
		case stFile:
			u.Scheme = "file"
			u.Host = ptr("")
			if c == '/' || c == '\\' {
				st = stFileSlash
			} else if base != nil && base.Scheme == "file" {
				u.Host = copyStr(base.Host)
				u.Path = copyPath(base.Path)
				u.Query = copyStr(base.Query)
				if c == '?' {
					u.Query = ptr("")
					st = stQuery
				} else if c == '#' {
					u.Fragment = ptr("")
					st = stFragment
				} else if c != eof {
					u.Query = nil
					if !p.startsWithDriveLetter(i) {
						p.shortenPath()
					} else {
						u.Path = nil
					}
					st = stPath
					i--
				}
			} else {
				st = stPath
				i--
			}
		case stFileSlash:
			if c == '/' || c == '\\' {
				st = stFileHost
			} else {
				if base != nil && base.Scheme == "file" {
					u.Host = copyStr(base.Host)
					if !p.startsWithDriveLetter(i) && len(base.Path) > 0 && isWindowsDriveLetter(base.Path[0], true) {
						u.Path = append(u.Path, base.Path[0])
					}
				}
				st = stPath
				i--
			}
		case stFileHost:
			if c == eof || c == '/' || c == '\\' || c == '?' || c == '#' {
				i--
				s := buf.String()
				if isWindowsDriveLetter(s, false) {
					st = stPath
				} else if s == "" {
					u.Host = ptr("")
					st = stPathStart
				} else {
					host, err := parseHost(s, false)
					if err != nil {
						return err
					}
					if host == "localhost" {
						host = ""
					}
					u.Host = &host
					buf.Reset()
					st = stPathStart
				}
			} else {
				buf.WriteRune(c)
			}
		case stPathStart:
			if u.IsSpecial() {
				st = stPath
				if c != '/' && c != '\\' {
					i--
				}
			} else if c == '?' {
				u.Query = ptr("")
				st = stQuery
			} else if c == '#' {
				u.Fragment = ptr("")
				st = stFragment
			} else if c != eof {
				st = stPath
				if c != '/' {
					i--
				}
			}
		case stPath:
			special := u.IsSpecial()
			if c == eof || c == '/' || special && c == '\\' || c == '?' || c == '#' {
				seg := buf.String()
				slash := c == '/' || special && c == '\\'
				switch {
				case isDoubleDot(seg):
					p.shortenPath()
					if !slash {
						u.Path = append(u.Path, "")
					}
				case isSingleDot(seg) && !slash:
					u.Path = append(u.Path, "")
				case !isSingleDot(seg):
					if u.Scheme == "file" && len(u.Path) == 0 && isWindowsDriveLetter(seg, false) {
						seg = seg[:1] + ":"
					}
					u.Path = append(u.Path, seg)
				}
				buf.Reset()
				if c == '?' {
					u.Query = ptr("")
					st = stQuery
				}
				if c == '#' {
					u.Fragment = ptr("")
					st = stFragment
				}
			} else {
				buf.WriteString(percentEncodeRune(c, inPathSet))
			}
		case stOpaquePath:
			if c == '?' {
				u.Query = ptr("")
				st = stQuery
			} else if c == '#' {
				u.Fragment = ptr("")
				st = stFragment
			} else if c == ' ' {
				// Ett blanksteg före ? eller # (eller sist) kodas, annars står det kvar.
				if n := p.at(i + 1); n == '?' || n == '#' || n == eof {
					*u.Opaque += "%20"
				} else {
					*u.Opaque += " "
				}
			} else if c != eof {
				*u.Opaque += percentEncodeRune(c, inC0ControlSet)
			}
		case stQuery:
			if c == '#' || c == eof {
				set := inQuerySet
				if u.IsSpecial() {
					set = inSpecialQuerySet
				}
				*u.Query += percentEncodeString(buf.String(), set)
				buf.Reset()
				if c == '#' {
					u.Fragment = ptr("")
					st = stFragment
				}
			} else {
				buf.WriteRune(c)
			}
		case stFragment:
			if c != eof {
				*u.Fragment += percentEncodeRune(c, inFragmentSet)
			}
		}
		if i >= len(p.in) {
			break
		}
	}
	if u.Opaque == nil && u.Path == nil {
		u.Path = []string{}
	}
	return nil
}

func copyStr(s *string) *string {
	if s == nil {
		return nil
	}
	return ptr(*s)
}

func isSingleDot(s string) bool { return s == "." || strings.EqualFold(s, "%2e") }

func isDoubleDot(s string) bool {
	switch strings.ToLower(s) {
	case "..", ".%2e", "%2e.", "%2e%2e":
		return true
	}
	return false
}

// Mängder av tecken som procentkodas, som i standarden.
func inC0ControlSet(c byte) bool { return c < 0x20 || c > 0x7E }
func inFragmentSet(c byte) bool {
	return inC0ControlSet(c) || c == ' ' || c == '"' || c == '<' || c == '>' || c == '`'
}
func inQuerySet(c byte) bool {
	return inC0ControlSet(c) || c == ' ' || c == '"' || c == '#' || c == '<' || c == '>'
}
func inSpecialQuerySet(c byte) bool { return inQuerySet(c) || c == '\'' }
func inPathSet(c byte) bool {
	return inQuerySet(c) || c == '?' || c == '^' || c == '`' || c == '{' || c == '}'
}
func inUserinfoSet(c byte) bool {
	return inPathSet(c) || c == '/' || c == ':' || c == ';' || c == '=' || c == '@' || c == '[' || c == '\\' || c == ']' || c == '|'
}

const hexUpper = "0123456789ABCDEF"

func percentEncodeString(s string, in func(byte) bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if in(c) {
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&15])
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func percentEncodeRune(r rune, in func(byte) bool) string {
	if r < 0x80 && !in(byte(r)) {
		return string(r)
	}
	return percentEncodeString(string(r), in)
}

// PercentDecode avkodar %XX till bytes; annat lämnas som det står.
func PercentDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isASCIIHex(rune(s[i+1])) && isASCIIHex(rune(s[i+2])) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			out = append(out, byte(v))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return out
}
