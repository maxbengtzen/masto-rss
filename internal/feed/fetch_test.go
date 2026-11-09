package feed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestFetchSendsValidatorsAndUnderstands304(t *testing.T) {
	var got http.Header
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		w.WriteHeader(304)
	})
	res, err := Fetch(context.Background(), http.DefaultClient, s.URL+"/feed", Validators{ETag: `"abc"`, LastModified: "Mon, 26 Oct 2026 08:00:00 GMT"})
	if err != nil || !res.NotModified || res.Redirect != nil {
		t.Fatalf("fick %+v, %v", res, err)
	}
	if got.Get("If-None-Match") != `"abc"` || got.Get("If-Modified-Since") != "Mon, 26 Oct 2026 08:00:00 GMT" || !strings.HasPrefix(got.Get("User-Agent"), "masto-rss/") {
		t.Errorf("huvuden: %v", got)
	}
}

func TestFetchReturnsTextAndValidators(t *testing.T) {
	var got http.Header
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Last-Modified", "Tue, 27 Oct 2026 08:00:00 GMT")
		fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
	})
	res, err := Fetch(context.Background(), http.DefaultClient, s.URL+"/feed", Validators{})
	if err != nil || res.NotModified || !strings.Contains(res.Text, "<rss") || res.ETag != `"v2"` || res.LastModified != "Tue, 27 Oct 2026 08:00:00 GMT" {
		t.Fatalf("fick %+v, %v", res, err)
	}
	if got.Get("If-None-Match") != "" {
		t.Error("skickade If-None-Match utan ETag")
	}
}

func TestFetchFollowsRedirects(t *testing.T) {
	var s *httptest.Server
	s = serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss":
			w.Header().Set("Location", "/rss/")
			w.WriteHeader(308)
		case "/a":
			w.Header().Set("Location", s.URL+"/b")
			w.WriteHeader(301)
		case "/b":
			w.Header().Set("Location", s.URL+"/c")
			w.WriteHeader(302)
		default:
			fmt.Fprint(w, "<rss></rss>")
		}
	})
	res, err := Fetch(context.Background(), http.DefaultClient, s.URL+"/rss", Validators{})
	if err != nil || *res.Redirect != (Redirect{To: s.URL + "/rss/", Permanent: true}) {
		t.Errorf("fick %+v, %v", res.Redirect, err)
	}
	res, err = Fetch(context.Background(), http.DefaultClient, s.URL+"/a", Validators{})
	if err != nil || *res.Redirect != (Redirect{To: s.URL + "/c", Permanent: false}) {
		t.Errorf("fick %+v, %v", res.Redirect, err)
	}
}

func TestFetchGivesUpAfterTooManyRedirects(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", r.URL.Path+"x")
		w.WriteHeader(302)
	})
	_, err := Fetch(context.Background(), http.DefaultClient, s.URL+"/loop", Validators{})
	if err == nil || err.Error() != "Fler än 5 omdirigeringar" {
		t.Errorf("fick %v", err)
	}
}

func TestFetchHTTPErrorWithRetryAfter(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(503)
		fmt.Fprint(w, "upptagen")
	})
	_, err := Fetch(context.Background(), http.DefaultClient, s.URL+"/feed", Validators{})
	var h *HTTPError
	if !errors.As(err, &h) || h.Status != 503 || h.RetryAfter != 120 || h.Error() != "HTTP 503 Service Unavailable" || h.Name() != "HttpError" {
		t.Errorf("fick %#v", err)
	}
}

func TestFetchStopsTooLargeResponses(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/header" {
			w.Header().Set("Content-Length", fmt.Sprint(MaxBytes+1))
			w.WriteHeader(200)
			return
		}
		chunk := make([]byte, 1024*1024)
		for i := 0; i < 20; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	for _, path := range []string{"/header", "/stream"} {
		_, err := Fetch(context.Background(), http.DefaultClient, s.URL+path, Validators{})
		if err == nil || !strings.Contains(err.Error(), "större än 5 MB") {
			t.Errorf("%s: fick %v", path, err)
		}
	}
}

func TestParseBrokenContentFails(t *testing.T) {
	if _, err := Parse("<html><body>Inte ett flöde</body></html>", "https://example.com/"); err == nil {
		t.Error("väntade fel")
	}
}
