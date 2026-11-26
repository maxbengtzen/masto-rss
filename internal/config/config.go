// Package config reads the configuration from environment variables.
//
// Each feed has a name, and its settings are called FEED_<NAME>_<FIELD>. A global value (the same
// field without a prefix, for example INTERVAL) applies to every feed that has no value of its own.
// Validation is strict: an unknown FEED_* variable or an invalid value stops startup, and all
// errors are reported at once.
package config

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

var (
	Visibilities   = []string{"public", "unlisted", "private", "direct"}
	QuotePolicies  = []string{"public", "followers", "nobody"}
	TemplateFields = []string{"title", "link", "summary", "author", "feed"}
)

// Feed holds a feed's settings.
type Feed struct {
	// Name is the name in lowercase, for example "news". The feed's identity in the database.
	Name string `json:"name"`
	// Prefix is the prefix in the environment variables, for example "FEED_NEWS_".
	Prefix string `json:"prefix"`
	URL    string `json:"url"`
	Token  string `json:"token"`
	// MastodonURL is the instance's URL without a trailing slash.
	MastodonURL string `json:"mastodonUrl"`
	IntervalMs  int64  `json:"intervalMs"`
	Visibility  string `json:"visibility"`
	// Language is an ISO 639 code. Empty: the feed's own language is used.
	Language string `json:"language,omitempty"`
	Template string `json:"template"`
	// QuotePolicy is empty when the account's default applies.
	QuotePolicy string `json:"quotePolicy,omitempty"`
	// MaxAgeMs 0 = no age limit.
	MaxAgeMs        int64 `json:"maxAgeMs"`
	MaxPostsPerPoll int   `json:"maxPostsPerPoll"`
	Backfill        int   `json:"backfill"`
}

// Config is the whole configuration.
type Config struct {
	// Feeds are the feeds to run, sorted by name.
	Feeds []Feed `json:"feeds"`
	// Paused holds the names of feeds with ENABLED=false. Their history is kept.
	Paused  []string `json:"paused"`
	DataDir string   `json:"dataDir"`
	DryRun  bool     `json:"dryRun"`
	Once    bool     `json:"once"`
	Debug   bool     `json:"debug"`
	// SignalURL and SignalNumber are signal-cli-rest-api for notifications; empty = off.
	SignalURL    string `json:"signalUrl"`
	SignalNumber string `json:"signalNumber"`
	// FailNotifyAfter: send a notification when a feed has failed this many times in a row.
	FailNotifyAfter int `json:"failNotifyAfter"`
	// StatusPort is the port for the status page (/status); 0 = off.
	StatusPort int `json:"statusPort"`
}

// Error holds all errors in the configuration.
type Error struct{ Problems []string }

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("Configuration errors:")
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return b.String()
}

// shared are fields that can be set both globally and per feed.
var shared = []string{"MASTODON_URL", "INTERVAL", "VISIBILITY", "TEMPLATE", "QUOTE_POLICY", "MAX_AGE", "MAX_POSTS_PER_POLL", "BACKFILL"}

// own are fields that exist only per feed. LANGUAGE belongs here because a global LANGUAGE is
// already a common system variable (gettext), for example "en_US:en".
var own = []string{"URL", "TOKEN", "ENABLED", "LANGUAGE"}

// The name is matched as short as possible, so that FEED_X_MASTODON_URL becomes feed X and field
// MASTODON_URL.
var feedKey = regexp.MustCompile(`^FEED_([A-Z0-9][A-Z0-9_]*?)_(` + strings.Join(append(append([]string{}, own...), shared...), "|") + `)$`)

const minIntervalMs = 60_000

var (
	digitsRE   = regexp.MustCompile(`^\d+$`)
	durationRE = regexp.MustCompile(`^(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$`)
	flagRE     = regexp.MustCompile(`^(?:1|[tT][rR][uU][eE]|[yY][eE][sS]|[jJ][aA]|[oO][nN])$`)
	boolRE     = regexp.MustCompile(`^(?:1|[tT][rR][uU][eE]|[yY][eE][sS]|[jJ][aA]|[oO][nN]|0|[fF][aA][lL][sS][eE]|[nN][oO]|[nN][eE][jJ]|[oO][fF][fF])$`)
	languageRE = regexp.MustCompile(`^([a-zA-Z]{2,3})(?:[-_][a-zA-Z0-9]{1,8})*$`)
	fieldRE    = regexp.MustCompile(`\{(\w+)\}`)
)

// number is Number(digits) in JavaScript, without overflowing.
func number(digits string) float64 {
	f, _ := strconv.ParseFloat(digits, 64)
	return f
}

func toMs(f float64) int64 {
	if f >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(f)
}

// ParseDuration parses "900" (seconds), "15m", "1h30m" or "2d" into milliseconds. ok is false if
// the text cannot be parsed.
func ParseDuration(text string) (ms int64, ok bool) {
	t := strings.ToLower(js.Trim(text))
	if digitsRE.MatchString(t) {
		return toMs(number(t) * 1000), true
	}
	m := durationRE.FindStringSubmatch(t)
	if t == "" || m == nil {
		return 0, false
	}
	days, hours, minutes, seconds := number(m[1]), number(m[2]), number(m[3]), number(m[4])
	return toMs((((days*24+hours)*60+minutes)*60 + seconds) * 1000), true
}

func flag(value string) bool { return flagRE.MatchString(js.Trim(value)) }

func httpURL(value string) *weburl.URL {
	u, err := weburl.Parse(value, nil)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return nil
	}
	return u
}

// orderedSet is a set that remembers insertion order, like Set in JavaScript.
type orderedSet struct {
	seen  map[string]bool
	items []string
}

func (s *orderedSet) add(v string) {
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	if !s.seen[v] {
		s.seen[v] = true
		s.items = append(s.items, v)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Load reads the configuration. environ is the environment in the order the process received it
// ("NAME=value", like os.Environ), since errors are reported in that order.
func Load(environ []string) (*Config, error) {
	env := map[string]string{}
	var problems orderedSet
	byName := map[string]map[string]string{}

	for _, kv := range environ {
		key, raw, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, dup := env[key]; dup {
			continue // like process.env: the first occurrence wins
		}
		env[key] = raw
		if !strings.HasPrefix(key, "FEED_") {
			continue
		}
		m := feedKey.FindStringSubmatch(key)
		if m == nil {
			problems.add("Unknown setting " + key + " (typo?)")
			continue
		}
		if byName[m[1]] == nil {
			byName[m[1]] = map[string]string{}
		}
		byName[m[1]][m[2]] = js.Trim(raw)
	}
	lookup := func(name string) (string, bool) { v, ok := env[name]; return v, ok }

	cfg := &Config{Feeds: []Feed{}, Paused: []string{}}
	envNames := make([]string, 0, len(byName))
	for name := range byName {
		envNames = append(envNames, name)
	}
	sort.Slice(envNames, func(i, j int) bool { return js.Less(envNames[i], envNames[j]) })

	for _, envName := range envNames {
		fields := byName[envName]
		prefix := "FEED_" + envName + "_"
		name := strings.ToLower(envName)
		enabled := fields["ENABLED"]
		if enabled != "" && !boolRE.MatchString(enabled) {
			problems.add(prefix + `ENABLED must be true or false, not "` + enabled + `"`)
		}
		if enabled != "" && !flag(enabled) {
			cfg.Paused = append(cfg.Paused, name)
			continue
		}

		failed := false
		fail := func(msg string) {
			failed = true
			problems.add(msg)
		}
		// pick returns the feed's own value, otherwise the global one, and the variable that supplied it.
		pick := func(field string) (value string, set bool, source string) {
			if v := fields[field]; v != "" {
				return v, true, prefix + field
			}
			g, _ := lookup(field)
			g = js.Trim(g)
			if g != "" {
				return g, true, field
			}
			return "", false, prefix + field
		}

		url := fields["URL"]
		if url == "" {
			fail(prefix + "URL is missing")
		} else if httpURL(url) == nil {
			fail(prefix + `URL is not an http(s) URL: "` + url + `"`)
		}
		token := fields["TOKEN"]
		if token == "" {
			fail(prefix + "TOKEN is missing")
		}

		instance, instanceSet, instanceSource := pick("MASTODON_URL")
		var instanceURL *weburl.URL
		if instanceSet {
			instanceURL = httpURL(instance)
		}
		if !instanceSet {
			fail("MASTODON_URL is missing (globally or as " + prefix + "MASTODON_URL)")
		} else if instanceURL == nil || instanceURL.Pathname() != "/" || instanceURL.Search() != "" {
			fail(instanceSource + " must be the instance's URL, for example https://mastodon.nu")
		}

		duration := func(field, fallback string, min int64) int64 {
			value, set, source := pick(field)
			if !set {
				value = fallback
			}
			ms, ok := ParseDuration(value)
			if !ok {
				fail(source + ` is not a duration: "` + value + `" (for example 900, 15m or 1h30m)`)
			} else if ms < min {
				fail(source + " is too short: at least " + strconv.FormatInt(min/1000, 10) + " s")
			}
			return ms
		}
		integer := func(field, fallback string, min float64) int {
			value, set, source := pick(field)
			if !set {
				value = fallback
			}
			if !digitsRE.MatchString(value) || number(value) < min {
				fail(source + " must be an integer, at least " + js.FormatNumber(min) + `: "` + value + `"`)
			}
			n := number(value)
			if n > math.MaxInt32 {
				return math.MaxInt32
			}
			return int(n)
		}
		oneOf := func(field string, allowed []string, fallback string) string {
			value, set, source := pick(field)
			if !set {
				return fallback
			}
			lower := strings.ToLower(value)
			if !contains(allowed, lower) {
				fail(source + " must be " + strings.Join(allowed, ", ") + `: "` + value + `"`)
			}
			return lower
		}

		intervalMs := duration("INTERVAL", "15m", minIntervalMs)
		maxAgeMs := duration("MAX_AGE", "48h", 0)
		maxPostsPerPoll := integer("MAX_POSTS_PER_POLL", "5", 1)
		backfill := integer("BACKFILL", "0", 0)
		visibility := oneOf("VISIBILITY", Visibilities, "public")
		quotePolicy := oneOf("QUOTE_POLICY", QuotePolicies, "")

		lang := fields["LANGUAGE"]
		var langMatch []string
		if lang != "" {
			langMatch = languageRE.FindStringSubmatch(lang)
			if langMatch == nil {
				fail(prefix + `LANGUAGE must be a language code such as sv or en: "` + lang + `"`)
			}
		}

		tpl, tplSet, tplSource := pick("TEMPLATE")
		if !tplSet {
			tpl = "{title}\n\n{link}"
		}
		// A \n in the value becomes a line break, so that the template can be written on one line in .env.
		template := strings.ReplaceAll(tpl, `\n`, "\n")
		for _, m := range fieldRE.FindAllStringSubmatch(template, -1) {
			if !contains(TemplateFields, m[1]) {
				valid := make([]string, len(TemplateFields))
				for i, f := range TemplateFields {
					valid[i] = "{" + f + "}"
				}
				fail(tplSource + " has unknown field {" + m[1] + "}; valid fields are " + strings.Join(valid, ", "))
			}
		}

		if failed {
			continue
		}
		feed := Feed{
			Name:            name,
			Prefix:          prefix,
			URL:             url,
			Token:           token,
			MastodonURL:     instanceURL.Origin(),
			IntervalMs:      intervalMs,
			Visibility:      visibility,
			Template:        template,
			QuotePolicy:     quotePolicy,
			MaxAgeMs:        maxAgeMs,
			MaxPostsPerPoll: maxPostsPerPoll,
			Backfill:        backfill,
		}
		if langMatch != nil {
			feed.Language = strings.ToLower(langMatch[1])
		}
		cfg.Feeds = append(cfg.Feeds, feed)
	}

	targets := map[string]string{}
	for _, feed := range cfg.Feeds {
		target := feed.URL + "\n" + feed.MastodonURL + "\n" + feed.Token
		if other, ok := targets[target]; ok {
			problems.add(other + " and " + feed.Prefix + " post the same feed to the same account")
		} else {
			targets[target] = feed.Prefix
		}
	}

	if len(cfg.Feeds) == 0 && len(cfg.Paused) == 0 && len(problems.items) == 0 {
		problems.add("No feeds: set at least FEED_<NAME>_URL and FEED_<NAME>_TOKEN")
	}

	numberEnv := func(name, fallback string, min, max float64) int {
		value, _ := lookup(name)
		value = js.Trim(value)
		if value == "" {
			value = fallback
		}
		n := number(value)
		if !digitsRE.MatchString(value) || n < min || n > max {
			problems.add(name + " must be an integer between " + js.FormatNumber(min) + " and " + js.FormatNumber(max) + `: "` + value + `"`)
		}
		if n > math.MaxInt32 {
			return math.MaxInt32
		}
		return int(n)
	}
	cfg.FailNotifyAfter = numberEnv("FAIL_NOTIFY_AFTER", "3", 1, math.Inf(1))
	cfg.StatusPort = numberEnv("STATUS_PORT", "8080", 0, 65535)
	signalURL, _ := lookup("SIGNAL_API_URL")
	cfg.SignalURL = strings.TrimRight(js.Trim(signalURL), "/")
	signalNumber, _ := lookup("SIGNAL_NUMBER")
	cfg.SignalNumber = js.Trim(signalNumber)
	if cfg.SignalURL != "" && httpURL(cfg.SignalURL) == nil {
		problems.add(`SIGNAL_API_URL is not an http(s) URL: "` + cfg.SignalURL + `"`)
	}
	if (cfg.SignalURL != "") != (cfg.SignalNumber != "") {
		problems.add("SIGNAL_API_URL and SIGNAL_NUMBER are both required for Signal notifications")
	}
	if len(problems.items) > 0 {
		return nil, &Error{Problems: problems.items}
	}

	dataDir, _ := lookup("DATA_DIR")
	cfg.DataDir = js.Trim(dataDir)
	if cfg.DataDir == "" {
		cfg.DataDir = "/data"
	}
	dryRun, _ := lookup("DRY_RUN")
	once, _ := lookup("ONCE")
	logLevel, _ := lookup("LOG_LEVEL")
	cfg.DryRun = flag(dryRun)
	cfg.Once = flag(once)
	cfg.Debug = strings.ToLower(js.Trim(logLevel)) == "debug"
	return cfg, nil
}
