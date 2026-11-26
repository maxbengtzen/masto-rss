package weburl_test

import (
	"testing"

	"github.com/maxbengtzen/masto-rss/internal/testutil"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

// new URL(link, base).href must be the same as in Node.
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
			t.Errorf("new URL(%q, %q): got %q, want an error", c.Link, c.Base, u.Href())
		case c.Href != nil && err != nil:
			t.Errorf("new URL(%q, %q): got an error, want %q", c.Link, c.Base, *c.Href)
		case c.Href != nil && u.Href() != *c.Href:
			t.Errorf("new URL(%q, %q): got %q, want %q", c.Link, c.Base, u.Href(), *c.Href)
		}
	}
}

func TestOriginAndParts(t *testing.T) {
	u, err := weburl.Parse("HTTPS://Mastodon.Example:443/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.Origin() != "https://mastodon.example" || u.Pathname() != "/" || u.Search() != "" {
		t.Errorf("got %q %q %q", u.Origin(), u.Pathname(), u.Search())
	}
	u, _ = weburl.Parse("http://[::1]:8443/x?y", nil)
	if u.Origin() != "http://[::1]:8443" || u.HostPort() != "[::1]:8443" || u.Search() != "?y" {
		t.Errorf("got %q %q %q", u.Origin(), u.HostPort(), u.Search())
	}
}

func TestDecodeUTF8(t *testing.T) {
	// Each invalid sequence becomes a replacement character, as in TextDecoder.
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
			t.Errorf("DecodeUTF8(%q) = %q, want %q", in, got, want)
		}
	}
}
