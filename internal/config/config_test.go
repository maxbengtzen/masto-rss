package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/maxbengtzen/masto-rss/internal/testutil"
)

var base = []string{
	"MASTODON_URL=https://mastodon.example/",
	"FEED_NEWS_URL=https://example.com/feed/",
	"FEED_NEWS_TOKEN=token-1",
}

func with(extra ...string) []string { return append(slices.Clone(base), extra...) }

func problems(t *testing.T, env []string) []string {
	t.Helper()
	_, err := Load(env)
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("väntade ett konfigurationsfel, fick %v", err)
	}
	return ce.Problems
}

func TestParseDuration(t *testing.T) {
	for text, want := range map[string]int64{"900": 900_000, "15m": 900_000, "1h30m": 5_400_000, "2d": 172_800_000, " 45S ": 45_000, "0": 0} {
		if ms, ok := ParseDuration(text); !ok || ms != want {
			t.Errorf("ParseDuration(%q) = %d, %v; väntade %d", text, ms, ok, want)
		}
	}
	for _, text := range []string{"", "15 min", "m15"} {
		if _, ok := ParseDuration(text); ok {
			t.Errorf("ParseDuration(%q) borde misslyckas", text)
		}
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(base)
	if err != nil {
		t.Fatal(err)
	}
	want := Feed{
		Name: "news", Prefix: "FEED_NEWS_", URL: "https://example.com/feed/", Token: "token-1",
		MastodonURL: "https://mastodon.example", IntervalMs: 900_000, Visibility: "public",
		Template: "{title}\n\n{link}", MaxAgeMs: 48 * 3600_000, MaxPostsPerPoll: 5,
	}
	if len(cfg.Feeds) != 1 || cfg.Feeds[0] != want {
		t.Errorf("fick %+v", cfg.Feeds)
	}
	if len(cfg.Paused) != 0 || cfg.DataDir != "/data" || cfg.DryRun || cfg.Once {
		t.Errorf("fick %+v", cfg)
	}
}

func TestGlobalAndOwnValues(t *testing.T) {
	cfg, err := Load(with("INTERVAL=30m", "VISIBILITY=unlisted", "FEED_OTHER_URL=https://example.org/rss", "FEED_OTHER_TOKEN=token-2",
		"FEED_OTHER_INTERVAL=1h", "FEED_OTHER_MASTODON_URL=https://other.example", "FEED_OTHER_LANGUAGE=sv-SE",
		"FEED_OTHER_QUOTE_POLICY=Nobody", "DRY_RUN=1", "ONCE=true", "DATA_DIR=/tmp/x"))
	if err != nil {
		t.Fatal(err)
	}
	news, other := cfg.Feeds[0], cfg.Feeds[1]
	if news.Name != "news" || news.IntervalMs != 1_800_000 || news.Visibility != "unlisted" {
		t.Errorf("news: %+v", news)
	}
	if other.Name != "other" || other.IntervalMs != 3_600_000 || other.MastodonURL != "https://other.example" || other.Language != "sv" || other.QuotePolicy != "nobody" {
		t.Errorf("other: %+v", other)
	}
	if !cfg.DryRun || !cfg.Once || cfg.DataDir != "/tmp/x" {
		t.Errorf("fick %+v", cfg)
	}
}

func TestNamesWithUnderscores(t *testing.T) {
	cfg, err := Load([]string{"MASTODON_URL=https://mastodon.example", "FEED_FASTIGHETS_SVERIGE_URL=https://example.com/feed",
		"FEED_FASTIGHETS_SVERIGE_TOKEN=t", "FEED_FASTIGHETS_SVERIGE_MAX_POSTS_PER_POLL=2"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Feeds[0].Name != "fastighets_sverige" || cfg.Feeds[0].MaxPostsPerPoll != 2 {
		t.Errorf("fick %+v", cfg.Feeds[0])
	}
}

func TestSmallerCases(t *testing.T) {
	cfg, _ := Load(with("LANGUAGE=en_US:en"))
	if cfg.Feeds[0].Language != "" {
		t.Error("en global LANGUAGE från systemet ska inte användas")
	}
	cfg, _ = Load(with(`TEMPLATE={title}\n{link} #nyheter`))
	if cfg.Feeds[0].Template != "{title}\n{link} #nyheter" {
		t.Errorf("\\n i mallen: %q", cfg.Feeds[0].Template)
	}
	cfg, _ = Load(with("MAX_AGE=0"))
	if cfg.Feeds[0].MaxAgeMs != 0 {
		t.Error("MAX_AGE=0 stänger av åldersspärren")
	}
	cfg, _ = Load(with("FEED_OLD_ENABLED=false", "FEED_OLD_URL=inte en adress"))
	if !reflect.DeepEqual(cfg.Paused, []string{"old"}) || len(cfg.Feeds) != 1 {
		t.Errorf("ENABLED=false pausar: %+v", cfg)
	}
}

func TestAllProblemsAtOnce(t *testing.T) {
	found := problems(t, []string{"FEED_NEWS_URL=ftp://example.com/feed", "FEED_NEWS_TEMPLTE={title}", "FEED_OTHER_URL=https://example.org/",
		"FEED_OTHER_INTERVAL=30", "FEED_OTHER_VISIBILITY=everyone", "FEED_OTHER_TEMPLATE={title} {body}", "FEED_OTHER_MAX_POSTS_PER_POLL=0"})
	for _, p := range []string{
		"Okänd inställning FEED_NEWS_TEMPLTE (stavfel?)",
		`FEED_NEWS_URL är ingen http(s)-adress: "ftp://example.com/feed"`,
		"FEED_NEWS_TOKEN saknas",
		"MASTODON_URL saknas (globalt eller som FEED_NEWS_MASTODON_URL)",
		"FEED_OTHER_TOKEN saknas",
		"FEED_OTHER_INTERVAL är för kort: minst 60 s",
		`FEED_OTHER_MAX_POSTS_PER_POLL måste vara ett heltal, minst 1: "0"`,
		`FEED_OTHER_VISIBILITY måste vara public, unlisted, private, direct: "everyone"`,
	} {
		if !slices.Contains(found, p) {
			t.Errorf("saknar: %s\nfick: %s", p, strings.Join(found, "\n"))
		}
	}
	if !slices.ContainsFunc(found, func(p string) bool { return strings.HasPrefix(p, "FEED_OTHER_TEMPLATE har okänt fält {body}") }) {
		t.Error("saknar okänt fält")
	}
}

func TestSingleProblems(t *testing.T) {
	cases := []struct {
		env  []string
		want []string
	}{
		{with("FEED_NEWS_ENABLED=kanske"), []string{`FEED_NEWS_ENABLED måste vara true eller false, inte "kanske"`}},
		{[]string{"MASTODON_URL=https://mastodon.example/@konto", "FEED_NEWS_URL=https://example.com/feed/", "FEED_NEWS_TOKEN=token-1"},
			[]string{"MASTODON_URL ska vara instansens adress, till exempel https://mastodon.nu"}},
		{with("FEED_COPY_URL=https://example.com/feed/", "FEED_COPY_TOKEN=token-1"), []string{"FEED_COPY_ och FEED_NEWS_ postar samma flöde till samma konto"}},
		{[]string{"MASTODON_URL=https://mastodon.example"}, []string{"Inga flöden: ange minst FEED_<NAMN>_URL och FEED_<NAMN>_TOKEN"}},
	}
	for _, c := range cases {
		if got := problems(t, c.env); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v:\nfick    %q\nväntade %q", c.env, got, c.want)
		}
	}
}

// Konfigurationen och felen ska bli desamma som i TypeScript-versionen, i samma ordning.
func TestConfigGolden(t *testing.T) {
	var golden struct {
		Durations []struct {
			Text string
			Ms   *int64
		}
		Configs []struct {
			Env      [][2]string
			Config   json.RawMessage
			Problems []string
		}
	}
	testutil.Golden(t, "config.json", &golden)
	for _, c := range golden.Durations {
		ms, ok := ParseDuration(c.Text)
		if c.Ms == nil && ok || c.Ms != nil && (!ok || ms != *c.Ms) {
			t.Errorf("ParseDuration(%q) = %d, %v; väntade %v", c.Text, ms, ok, c.Ms)
		}
	}
	for _, c := range golden.Configs {
		var env []string
		for _, kv := range c.Env {
			env = append(env, kv[0]+"="+kv[1])
		}
		cfg, err := Load(env)
		if c.Problems != nil {
			var ce *Error
			if !errors.As(err, &ce) || !reflect.DeepEqual(ce.Problems, c.Problems) {
				t.Errorf("%v:\nfick    %v\nväntade %q", env, err, c.Problems)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", env, err)
			continue
		}
		got, _ := json.Marshal(cfg)
		var g, w any
		json.Unmarshal(got, &g)
		json.Unmarshal(c.Config, &w)
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%v:\nfick    %s\nväntade %s", env, got, c.Config)
		}
	}
}
