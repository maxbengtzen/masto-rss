package feed

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"

	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

// Item är en post i flödet.
type Item struct {
	// Keys är nycklarna för dubblettkontrollen: "g:" + GUID och "l:" + normaliserad länk.
	Keys    []string `json:"keys"`
	Title   string   `json:"title"`
	Link    string   `json:"link,omitempty"`
	Summary string   `json:"summary"`
	Author  string   `json:"author,omitempty"`
	// Published är publiceringstiden i ms, om flödet anger en giltig.
	Published *int64 `json:"published,omitempty"`
}

// Parsed är ett tolkat flöde.
type Parsed struct {
	Title string `json:"title,omitempty"`
	// Language är flödets språk som ISO 639-kod, till exempel "sv".
	Language string  `json:"language,omitempty"`
	Items    []*Item `json:"items"`
	// Skipped är poster utan både id och länk. De går inte att känna igen och hoppas över.
	Skipped int `json:"skipped"`
}

// raw är en post som formatet beskriver den, innan den görs om till Item.
type raw struct{ id, link, title, summary, author, date string }

// Parse tolkar ett flöde i valfritt format. base är adressen som relativa länkar utgår från.
//
// Tolkningen ska ge samma resultat som TypeScript-versionen, som använde feedsmith: samma format
// känns igen i samma ordning (RSS, Atom, RDF, JSON Feed), och samma fält läses på samma sätt.
func Parse(text, base string) (*Parsed, error) {
	var title, language string
	var raws []raw
	var err error
	switch {
	case detectRSS(text):
		title, language, raws, err = parseRSS(text)
	case detectAtom(text):
		title, language, raws, err = parseAtom(text)
	case detectRDF(text):
		title, language, raws, err = parseRDF(text)
	default:
		obj, ok := jsonObject(text)
		if !ok || !detectJSON(obj) {
			return nil, errUnrecognized
		}
		title, language, raws, err = parseJSON(obj)
	}
	if err != nil {
		return nil, err
	}

	out := &Parsed{Items: []*Item{}, Language: languageCode(language)}
	if title != "" {
		out.Title = HTMLToText(title)
	}
	for _, r := range raws {
		if item := toItem(r, base); item != nil {
			out.Items = append(out.Items, item)
		}
	}
	out.Skipped = len(raws) - len(out.Items)
	return out, nil
}

func toItem(r raw, base string) *Item {
	id := js.Trim(r.id)
	link := js.Trim(r.link)
	if link != "" {
		if u, err := weburl.ParseRef(link, base); err == nil {
			link = u.Href()
		}
	}
	var keys []string
	if id != "" {
		keys = append(keys, "g:"+id)
	}
	if link != "" {
		keys = append(keys, "l:"+NormalizeLink(link))
	}
	if len(keys) == 0 {
		return nil
	}
	item := &Item{Keys: keys, Title: HTMLToText(r.title), Link: link, Summary: HTMLToText(r.summary)}
	if r.author != "" {
		item.Author = HTMLToText(r.author)
	}
	if r.date != "" {
		if ms, ok := js.ParseDate(r.date); ok {
			item.Published = &ms
		}
	}
	return item
}

var languageRE = regexp.MustCompile(`^([a-zA-Z]{2,3})(?:[-_][a-zA-Z0-9]{1,8})*$`)

func languageCode(value string) string {
	m := languageRE.FindStringSubmatch(js.Trim(value))
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// --- Igenkänning, som feedsmiths detect --------------------------------------------------

const jsSpace = `\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

var (
	rssElementRE   = regexp.MustCompile(`(?:^|[` + jsSpace + `>])<(?:\w+:)?` + ci("rss") + `[` + jsSpace + `>]`)
	versionAttrRE  = regexp.MustCompile(ci(`version`) + `[` + jsSpace + `]*=[` + jsSpace + `]*["'][^"']*["']`)
	rssElementsRE  = regexp.MustCompile(`<(?:` + ci("channel|item|title|link|description|pubdate|guid") + `)[` + jsSpace + `>]`)
	feedElementRE  = regexp.MustCompile(`(?:^|[` + jsSpace + `>])<(?:\w+:)?` + ci("feed") + `[` + jsSpace + `>]`)
	atomElementsRE = regexp.MustCompile(`<(?:\w+:)?(?:` + ci("entry|title|link|id|updated|summary") + `)[` + jsSpace + `>]`)
	rdfElementRE   = regexp.MustCompile(`(?:^|[` + jsSpace + `>])<(?:\w+:)?` + ci("rdf") + `[` + jsSpace + `>]`)
	rdfElementsRE  = regexp.MustCompile(`<(?:\w+:)?(?:` + ci("channel|item|title|link|description") + `)[` + jsSpace + `>]`)
)

var atomURIs = []string{
	"http://www.w3.org/2005/Atom", "https://www.w3.org/2005/Atom", "http://www.w3.org/2005/Atom/",
	"https://www.w3.org/2005/Atom/", "http://purl.org/atom/ns#", "https://purl.org/atom/ns#",
}

var rdfURIs = []string{
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#", "https://www.w3.org/1999/02/22-rdf-syntax-ns#",
	"http://purl.org/rss/1.0/", "https://purl.org/rss/1.0/", "http://purl.org/rss/1.0", "https://purl.org/rss/1.0",
	"http://channel.netscape.com/rdf/simple/0.9/", "https://channel.netscape.com/rdf/simple/0.9/",
	"http://channel.netscape.com/rdf/simple/0.9", "https://channel.netscape.com/rdf/simple/0.9",
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func detectRSS(s string) bool {
	return s != "" && rssElementRE.MatchString(s) && (versionAttrRE.MatchString(s) || rssElementsRE.MatchString(s))
}

func detectAtom(s string) bool {
	return s != "" && feedElementRE.MatchString(s) && (containsAny(s, atomURIs) || atomElementsRE.MatchString(s))
}

func detectRDF(s string) bool {
	return s != "" && rdfElementRE.MatchString(s) && (containsAny(s, rdfURIs) || rdfElementsRE.MatchString(s))
}

// --- RSS 2.0 (och 0.9x) -----------------------------------------------------------------

func parseRSS(text string) (title, language string, items []raw, err error) {
	root, err := buildTree(text, nil, stopPathsRSS)
	if err != nil {
		return "", "", nil, err
	}
	rss := root.first("rss")
	channel := rss.first("channel")
	if !channel.isObject() {
		return "", "", nil, errInvalid
	}
	title = channel.first("title").str()
	language = channel.first("language").str()
	if language == "" {
		language = firstNonEmpty(channel.all("dc:language"))
	}
	nodes := validItems(channel.all("item"), rssItemHasContent)
	if len(nodes) == 0 {
		nodes = validItems(rss.all("item"), rssItemHasContent)
	}
	for _, n := range nodes {
		r := raw{
			id:    n.first("guid").str(),
			link:  n.first("link").str(),
			title: n.first("title").str(),
			date:  n.first("pubdate").str(),
		}
		r.summary = n.first("description").str()
		if r.summary == "" {
			r.summary = n.first("content:encoded").str()
		}
		r.author = firstNonEmpty(n.all("dc:creator"))
		if r.author == "" {
			if p := firstPerson(n.all("author")); p != nil {
				r.author = p.name
				if r.author == "" {
					r.author = p.email
				}
			}
		}
		if r.date == "" {
			r.date = firstNonEmpty(n.all("dc:date"))
		}
		items = append(items, r)
	}
	if title == "" && len(items) == 0 && !channelHasContent(channel) {
		return "", "", nil, errInvalid
	}
	return title, language, items, nil
}

func firstNonEmpty(nodes []*xnode) string {
	for _, n := range nodes {
		if s := n.str(); s != "" {
			return s
		}
	}
	return ""
}

// validItems är parseArrayOf(…, parseItem): bara poster som blev objekt med något i.
func validItems(nodes []*xnode, hasContent func(*xnode) bool) []*xnode {
	var out []*xnode
	for _, n := range nodes {
		if n.isObject() && hasContent(n) {
			out = append(out, n)
		}
	}
	return out
}

// hasContent är sant om något barn eller attribut ger ett värde. feedsmith läser många fler fält
// än de som används här, och en post utan något av dem räknas inte alls.
func hasContent(n *xnode, known map[string]bool) bool {
	for name := range n.attrs {
		if strings.HasPrefix(name, "@xml:") && parseString(n.attrs[name]) != "" {
			return true
		}
	}
	for _, c := range n.children {
		prefix, _, namespaced := strings.Cut(c.name, ":")
		if !known[c.name] && !(namespaced && knownPrefix[prefix]) {
			continue
		}
		if c.str() != "" || len(c.attrs) > 0 || len(c.children) > 0 {
			return true
		}
	}
	return false
}

var knownPrefix = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range namespacePrefixes {
		m[p] = true
	}
	return m
}()

var rssItemFields = map[string]bool{"title": true, "link": true, "description": true, "author": true, "category": true, "comments": true, "enclosure": true, "guid": true, "pubdate": true, "source": true}
var rssChannelFields = map[string]bool{
	"title": true, "link": true, "description": true, "language": true, "copyright": true, "managingeditor": true,
	"webmaster": true, "pubdate": true, "lastbuilddate": true, "category": true, "generator": true, "docs": true,
	"cloud": true, "ttl": true, "image": true, "rating": true, "textinput": true, "skiphours": true, "skipdays": true, "item": true,
}

func rssItemHasContent(n *xnode) bool { return hasContent(n, rssItemFields) }
func channelHasContent(n *xnode) bool { return hasContent(n, rssChannelFields) }

// person är en författare som feedsmith tolkar ett RSS-<author> (namn, e-post, länk).
type person struct{ name, email, link string }

func firstPerson(nodes []*xnode) *person {
	for _, n := range nodes {
		if p := parsePerson(n); p != nil {
			return p
		}
	}
	return nil
}

var (
	emailRE         = regexp.MustCompile(`^[^` + jsSpace + `@()<>\[\]]+@[^` + jsSpace + `@()<>\[\]]+\.[^` + jsSpace + `@()<>\[\]]+$`)
	personURLRE     = regexp.MustCompile(`^(?:https?://|www\.)[^` + jsSpace + `]+\.[^` + jsSpace + `]+$`)
	mailtoRE        = regexp.MustCompile(`^` + ci("mailto") + `:`)
	separatorsRE    = regexp.MustCompile(`^[` + jsSpace + `,\-|/:;]+|[` + jsSpace + `,\-|/:;]+$`)
	whitespaceRunRE = regexp.MustCompile(`[` + jsSpace + `]+`)
)

func stripMailto(s string) string {
	stripped := mailtoRE.ReplaceAllString(s, "")
	if stripped != s {
		if i := strings.IndexByte(stripped, '?'); i >= 0 {
			stripped = stripped[:i]
		}
	}
	return stripped
}

// parsePerson är feedsmiths tolkning av ett RSS-<author>, till exempel "anna@example.com (Anna)".
func parsePerson(n *xnode) *person {
	var rawText string
	if name := n.first("name"); name != nil {
		rawText = name.str()
	} else {
		rawText = n.str()
	}
	if rawText == "" {
		return nil
	}
	s := stripMailto(rawText)
	if personURLRE.MatchString(s) {
		return &person{link: s}
	}
	if emailRE.MatchString(s) {
		return &person{email: s}
	}
	if !strings.ContainsAny(rawText, "<[(") {
		return unbracketedPerson(rawText)
	}
	return bracketedPerson(rawText)
}

func unbracketedPerson(s string) *person {
	if !strings.Contains(s, "@") && !strings.Contains(s, "://") && !strings.Contains(s, "www.") {
		return &person{name: s}
	}
	var names []string
	var p person
	for _, word := range whitespaceRunRE.Split(s, -1) {
		stripped := stripMailto(word)
		switch {
		case personURLRE.MatchString(word) && p.link == "":
			p.link = word
		case emailRE.MatchString(stripped) && p.email == "":
			p.email = stripped
		default:
			names = append(names, word)
		}
	}
	if p.email == "" && p.link == "" {
		return &person{name: s}
	}
	p.name = parseString(separatorsRE.ReplaceAllString(strings.Join(names, " "), ""))
	return &p
}

func bracketedPerson(s string) *person {
	var p person
	var names []string
	unbracketedName := false
	r := []rune(s)
	isOpen := func(c rune) bool { return c == '<' || c == '(' || c == '[' }
	for i := 0; i < len(r); {
		var chunk string
		bracketed := false
		var open, close rune
		if isOpen(r[i]) {
			open = r[i]
			close = map[rune]rune{'<': '>', '(': ')', '[': ']'}[open]
			start := i + 1
			depth, end := 1, start
			for end < len(r) && depth > 0 {
				if r[end] == open {
					depth++
				} else if r[end] == close {
					depth--
				}
				end++
			}
			if depth == 0 {
				bracketed = true
				chunk = parseString(string(r[start : end-1]))
				i = end
			} else {
				literalStart := i
				i = start
				for i < len(r) && !isOpen(r[i]) {
					i++
				}
				chunk = parseString(string(r[literalStart:i]))
			}
		} else {
			start := i
			for i < len(r) && !isOpen(r[i]) {
				i++
			}
			chunk = parseString(string(r[start:i]))
		}
		if chunk == "" {
			continue
		}
		stripped := stripMailto(chunk)
		switch {
		case personURLRE.MatchString(chunk) && p.link == "":
			p.link = chunk
		case emailRE.MatchString(stripped) && p.email == "":
			p.email = stripped
		case bracketed:
			if unbracketedName {
				names = append(names, string(open)+chunk+string(close))
			} else {
				names = append(names, chunk)
			}
		default:
			unbracketedName = true
			names = append(names, chunk)
		}
	}
	p.name = parseString(strings.Join(names, " "))
	if p == (person{}) {
		return nil
	}
	return &p
}

// --- Atom -------------------------------------------------------------------------------

func parseAtom(text string) (title, language string, items []raw, err error) {
	root, err := buildTree(text, []string{"atom"}, stopPathsATOM)
	if err != nil {
		return "", "", nil, err
	}
	feed := root.first("feed")
	if feed == nil {
		feed = root.first("atom:feed")
	}
	if !feed.isObject() {
		return "", "", nil, errInvalid
	}
	title = atomText(feed.first("title"))
	language = feed.attr("xml:lang")
	for _, e := range feed.all("entry") {
		if !e.isObject() || !hasContent(e, atomEntryFields) {
			continue
		}
		r := raw{id: e.first("id").str(), title: atomText(e.first("title"))}
		var links []*xnode
		for _, l := range e.all("link") {
			if l.isObject() && (l.str() != "" || anyAttr(l, "href", "rel", "type", "hreflang", "title", "length")) {
				links = append(links, l)
			}
		}
		var link *xnode
		for _, l := range links {
			if rel := l.attr("rel"); rel == "" || rel == "alternate" {
				link = l
				break
			}
		}
		if link == nil && len(links) > 0 {
			link = links[0]
		}
		if link != nil {
			r.link = link.attr("href")
			if r.link == "" {
				r.link = link.str()
			}
		}
		r.summary = atomText(e.first("summary"))
		if r.summary == "" {
			r.summary = atomText(e.first("content"))
		}
		for _, a := range e.all("author") {
			if a.isObject() {
				r.author = a.first("name").str()
				if r.author != "" || hasPersonContent(a) {
					break
				}
			}
		}
		r.date = firstNonEmpty([]*xnode{e.first("published"), e.first("issued"), e.first("created")})
		if r.date == "" {
			r.date = firstNonEmpty([]*xnode{e.first("updated"), e.first("modified")})
		}
		items = append(items, r)
	}
	if title == "" && len(items) == 0 && !hasContent(feed, atomFeedFields) {
		return "", "", nil, errInvalid
	}
	return title, language, items, nil
}

func anyAttr(n *xnode, names ...string) bool {
	for _, name := range names {
		if n.attr(name) != "" {
			return true
		}
	}
	return false
}

// hasPersonContent är sant när en atom-författare blev ett objekt i feedsmith (har namn, adress
// eller e-post); bara då räknas den som första författaren.
func hasPersonContent(n *xnode) bool {
	for _, f := range []string{"name", "uri", "url", "email"} {
		if n.first(f).str() != "" {
			return true
		}
	}
	return false
}

var atomEntryFields = map[string]bool{
	"author": true, "category": true, "content": true, "contributor": true, "id": true, "link": true,
	"published": true, "issued": true, "created": true, "rights": true, "source": true, "summary": true,
	"title": true, "updated": true, "modified": true,
}
var atomFeedFields = map[string]bool{
	"author": true, "category": true, "contributor": true, "generator": true, "icon": true, "id": true,
	"link": true, "logo": true, "rights": true, "subtitle": true, "tagline": true, "title": true,
	"updated": true, "modified": true, "entry": true,
}

var xhtmlDivRE = regexp.MustCompile(`^\s*<(?:([a-zA-Z][\w.-]*):)?div[\s/>]`)
var cdataSectionRE = regexp.MustCompile(`<!\[CDATA\[([\s\S]*?)\]\]>`)

// atomText är värdet av en textkonstruktion (title, summary, content): text, html eller xhtml.
func atomText(n *xnode) string {
	if n == nil {
		return ""
	}
	if !n.isObject() {
		return n.str()
	}
	return typedText(n)
}

func typedText(n *xnode) string {
	text, ok := n.rawText()
	typ := n.attr("type")
	if typ != "xhtml" && typ != "application/xhtml+xml" {
		if !ok {
			return ""
		}
		return parseString(text)
	}
	if !ok {
		return ""
	}
	// XHTML: innehållet i den omslutande <div> används som det står, utan avkodning.
	escaped := cdataSectionRE.ReplaceAllStringFunc(text, func(m string) string {
		inner := m[9 : len(m)-3]
		return strings.ReplaceAll(strings.ReplaceAll(inner, "&", "&amp;"), "<", "&lt;")
	})
	if inner, ok := unwrapXHTMLDiv(escaped); ok {
		return js.Trim(inner)
	}
	if escaped != "" && xhtmlDivRE.MatchString(escaped) {
		return js.Trim(escaped)
	}
	return parseString(text)
}

// unwrapXHTMLDiv tar fram innehållet i en ensam omslutande <div>, med div-prefixet borttaget från
// taggarna inuti.
func unwrapXHTMLDiv(s string) (string, bool) {
	m := xhtmlDivRE.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	prefix := m[1]
	tag := "div"
	if prefix != "" {
		tag = prefix + ":div"
	}
	trimmed := strings.TrimSpace(s)
	open := strings.Index(trimmed, ">")
	closeTag := "</" + tag
	end := strings.LastIndex(trimmed, closeTag)
	if open < 0 || end < open || !strings.HasSuffix(strings.TrimSpace(trimmed[end+len(closeTag):]), ">") ||
		strings.TrimSpace(trimmed[end+len(closeTag):]) != ">" {
		return "", false
	}
	if strings.HasSuffix(trimmed[:open], "/") {
		return "", false
	}
	inner := trimmed[open+1 : end]
	if prefix != "" {
		inner = strings.NewReplacer("<"+prefix+":", "<", "</"+prefix+":", "</").Replace(inner)
	}
	return inner, true
}

// --- RDF (RSS 1.0 och 0.90) -------------------------------------------------------------

func parseRDF(text string) (title, language string, items []raw, err error) {
	root, err := buildTree(text, []string{"rdf", "rss"}, stopPathsRDF)
	if err != nil {
		return "", "", nil, err
	}
	rdf := root.first("rdf")
	if !rdf.isObject() {
		return "", "", nil, errInvalid
	}
	channel := rdf.first("channel")
	title = channel.first("title").str()
	language = firstNonEmpty(channel.all("dc:language"))

	// Ordningen kommer från kanalens <items><rdf:Seq>, om den finns.
	var nodes []*xnode
	var seq *xnode
	if itemsNode := channel.first("items"); itemsNode != nil {
		seq = itemsNode.first("seq")
	}
	if seq != nil {
		all := rdf.all("item")
		for _, li := range seq.all("li") {
			uri := li.attr("resource")
			if uri == "" {
				continue
			}
			for _, it := range all {
				if it.attrs["@about"] == uri {
					if it.isObject() && rdfItemHasContent(it) {
						nodes = append(nodes, it)
					}
					break
				}
			}
		}
	}
	if len(nodes) == 0 {
		nodes = validItems(rdf.all("item"), rdfItemHasContent)
	}
	for _, n := range nodes {
		r := raw{
			id:    n.attr("about"),
			link:  n.first("link").str(),
			title: n.first("title").str(),
		}
		if r.id == "" {
			r.id = n.attr("rdf:about")
		}
		r.summary = n.first("description").str()
		if r.summary == "" {
			r.summary = n.first("content:encoded").str()
		}
		r.author = firstNonEmpty(n.all("dc:creator"))
		r.date = firstNonEmpty(n.all("dc:date"))
		items = append(items, r)
	}
	if title == "" && len(items) == 0 && !hasContent(rdf, rdfFields) && !hasContent(channel, rdfFields) {
		return "", "", nil, errInvalid
	}
	return title, language, items, nil
}

var rdfItemFields = map[string]bool{"title": true, "link": true, "description": true}
var rdfFields = map[string]bool{"title": true, "link": true, "description": true, "channel": true, "item": true, "image": true, "textinput": true}

func rdfItemHasContent(n *xnode) bool {
	return n.attr("about") != "" || n.attr("rdf:about") != "" || hasContent(n, rdfItemFields)
}

// --- JSON Feed --------------------------------------------------------------------------

// jsonObject är feedsmiths parseJsonObject: texten måste se ut som ett JSON-objekt.
func jsonObject(text string) (map[string]any, bool) {
	t := strings.TrimFunc(text, js.IsSpace)
	if len(text) < 2 || !(strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") || strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")) {
		return nil, false
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return nil, false
	}
	obj, ok := v.(map[string]any)
	return obj, ok
}

// get är feedsmiths skiftlägesokänsliga uppslag.
func get(obj map[string]any, key string) any {
	if v, ok := obj[key]; ok {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range obj {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return nil
}

func detectJSON(obj map[string]any) bool {
	if v, ok := get(obj, "version").(string); ok && v != "" {
		return strings.Contains(v, "jsonfeed.org/version/")
	}
	title, _ := get(obj, "title").(string)
	_, hasItems := get(obj, "items").([]any)
	return title != "" && (hasItems || truthy(get(obj, "home_page_url")) || truthy(get(obj, "feed_url")) || truthy(get(obj, "authors")))
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	}
	return true
}

// verbatim är parseVerbatimString: en sträng trimmad, ett tal som text, annat inget. En lista ger
// sitt första värde (parseSingular).
func verbatim(v any) string {
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return ""
		}
		v = list[0]
	}
	switch v := v.(type) {
	case string:
		return js.Trim(v)
	case float64:
		return js.FormatNumber(v)
	}
	return ""
}

func jsonDate(v any) string {
	if list, ok := v.([]any); ok {
		if len(list) == 0 {
			return ""
		}
		v = list[0]
	}
	switch v := v.(type) {
	case string:
		return parseString(v)
	case float64:
		return js.FormatNumber(v)
	}
	return ""
}

func parseJSON(obj map[string]any) (title, language string, items []raw, err error) {
	title = verbatim(get(obj, "title"))
	language = verbatim(get(obj, "language"))
	list, _ := get(obj, "items").([]any)
	if list == nil {
		if v := get(obj, "items"); v != nil {
			list = []any{v}
		}
	}
	for _, v := range list {
		item, ok := v.(map[string]any)
		if !ok {
			continue
		}
		r := raw{
			id:    verbatim(get(item, "id")),
			title: verbatim(get(item, "title")),
		}
		r.link = verbatim(get(item, "url"))
		if r.link == "" {
			r.link = verbatim(get(item, "external_url"))
		}
		for _, key := range []string{"summary", "content_text", "content_html"} {
			if r.summary = verbatim(get(item, key)); r.summary != "" {
				break
			}
		}
		r.author = jsonAuthor(item)
		r.date = jsonDate(get(item, "date_published"))
		if r.date == "" {
			r.date = jsonDate(get(item, "date_modified"))
		}
		if !jsonItemHasContent(item, r) {
			continue
		}
		items = append(items, r)
	}
	if title == "" && len(items) == 0 && !jsonFeedHasContent(obj) {
		return "", "", nil, errInvalid
	}
	return title, language, items, nil
}

func jsonFeedHasContent(obj map[string]any) bool {
	for _, key := range []string{"home_page_url", "feed_url", "description", "user_comment", "next_url", "icon", "favicon", "language"} {
		if verbatim(get(obj, key)) != "" {
			return true
		}
	}
	if _, ok := get(obj, "expired").(bool); ok {
		return true
	}
	return len(jsonAuthors(get(obj, "authors"))) > 0 || len(jsonAuthors(get(obj, "author"))) > 0 || truthy(get(obj, "hubs"))
}

func jsonAuthors(v any) []string {
	list, ok := v.([]any)
	if !ok {
		if v == nil {
			return nil
		}
		list = []any{v}
	}
	var out []string
	for _, a := range list {
		switch a := a.(type) {
		case map[string]any:
			name := verbatim(get(a, "name"))
			if name != "" || verbatim(get(a, "url")) != "" || verbatim(get(a, "avatar")) != "" {
				out = append(out, name)
			}
		case string, float64:
			if s := verbatim(a); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func jsonAuthor(item map[string]any) string {
	authors := jsonAuthors(get(item, "authors"))
	if len(authors) == 0 {
		authors = jsonAuthors(get(item, "author"))
	}
	if len(authors) == 0 {
		return ""
	}
	return authors[0]
}

func jsonItemHasContent(item map[string]any, r raw) bool {
	if r.id != "" || r.link != "" || r.title != "" || r.summary != "" || r.date != "" || r.author != "" {
		return true
	}
	for _, key := range []string{"external_url", "content_html", "content_text", "image", "banner_image", "language"} {
		if verbatim(get(item, key)) != "" {
			return true
		}
	}
	return false
}
