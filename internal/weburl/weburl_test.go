package weburl_test

import (
	"testing"

	"github.com/maxbengtzen/masto-rss/internal/testutil"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

// new URL(link, base).href ska bli detsamma som i Node.
func TestResolveGolden(t *testing.T) {
	var golden struct {
		Resolve []struct {
			Base, Link string
			Href       *string
		}
	}
	testutil.Golden(t, "text.json", &golden)
	for _, c := range golden.Resolve {
		u, err := weburl.ParseRef(c.Link, c.Base)
		switch {
		case c.Href == nil && err == nil:
			t.Errorf("new URL(%q, %q): fick %q, väntade fel", c.Link, c.Base, u.Href())
		case c.Href != nil && err != nil:
			t.Errorf("new URL(%q, %q): fick fel, väntade %q", c.Link, c.Base, *c.Href)
		case c.Href != nil && u.Href() != *c.Href:
			t.Errorf("new URL(%q, %q): fick %q, väntade %q", c.Link, c.Base, u.Href(), *c.Href)
		}
	}
}

func TestOriginAndParts(t *testing.T) {
	u, err := weburl.Parse("HTTPS://Mastodon.Example:443/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin() != "https://mastodon.example" || u.Pathname() != "/" || u.Search() != "" {
		t.Errorf("fick %q %q %q", u.Origin(), u.Pathname(), u.Search())
	}
	u, _ = weburl.Parse("http://[::1]:8443/x?y", nil)
	if u.Origin() != "http://[::1]:8443" || u.HostPort() != "[::1]:8443" || u.Search() != "?y" {
		t.Errorf("fick %q %q %q", u.Origin(), u.HostPort(), u.Search())
	}
}

func TestDecodeUTF8(t *testing.T) {
	// Varje ogiltig del blir ett ersättningstecken, som i TextDecoder.
	for in, want := range map[string]string{
		"\xe2\x82<":         "\uFFFD<",
		"\xc3(":             "\uFFFD(",
		"\xf0\x9f\x98\xff":  "\uFFFD\uFFFD",
		"\xc0\xaf":          "\uFFFD\uFFFD",
		"\xed\xa0\x80":      "\uFFFD\uFFFD\uFFFD",
		"\xe2\x82\xac":      "€",
		"\xf0\x9f\x98\x80x": "😀x",
	} {
		if got := weburl.DecodeUTF8([]byte(in)); got != want {
			t.Errorf("DecodeUTF8(%q) = %q, väntade %q", in, got, want)
		}
	}
}
