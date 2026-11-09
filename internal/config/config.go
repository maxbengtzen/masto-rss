// Package config läser konfigurationen ur miljövariabler.
//
// Varje flöde har ett namn, och dess inställningar heter FEED_<NAMN>_<FÄLT>. Ett globalt värde
// (samma fält utan prefix, till exempel INTERVAL) gäller alla flöden som inte har ett eget.
// Kontrollen är strikt: en okänd FEED_*-variabel eller ett ogiltigt värde stoppar starten, och
// alla fel rapporteras på en gång.
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

// Feed är ett flödes inställningar.
type Feed struct {
	// Name är namnet med gemener, till exempel "feber". Flödets identitet i databasen.
	Name string `json:"name"`
	// Prefix är prefixet i miljövariablerna, till exempel "FEED_FEBER_".
	Prefix string `json:"prefix"`
	URL    string `json:"url"`
	Token  string `json:"token"`
	// MastodonURL är instansens adress utan avslutande snedstreck.
	MastodonURL string `json:"mastodonUrl"`
	IntervalMs  int64  `json:"intervalMs"`
	Visibility  string `json:"visibility"`
	// Language är en ISO 639-kod. Tom: flödets eget språk används.
	Language string `json:"language,omitempty"`
	Template string `json:"template"`
	// QuotePolicy är tom när kontots standard gäller.
	QuotePolicy string `json:"quotePolicy,omitempty"`
	// MaxAgeMs 0 = ingen åldersgräns.
	MaxAgeMs        int64 `json:"maxAgeMs"`
	MaxPostsPerPoll int   `json:"maxPostsPerPoll"`
	Backfill        int   `json:"backfill"`
}

// Config är hela konfigurationen.
type Config struct {
	// Feeds är flödena som ska köras, sorterade efter namn.
	Feeds []Feed `json:"feeds"`
	// Paused är namnen på flöden med ENABLED=false. Deras historik behålls.
	Paused  []string `json:"paused"`
	DataDir string   `json:"dataDir"`
	DryRun  bool     `json:"dryRun"`
	Once    bool     `json:"once"`
	Debug   bool     `json:"debug"`
	// SignalURL och SignalNumber är signal-cli-rest-api för aviseringar; tomma = av.
	SignalURL    string `json:"signalUrl"`
	SignalNumber string `json:"signalNumber"`
	// FailNotifyAfter: avisera när ett flöde misslyckats så här många gånger i rad.
	FailNotifyAfter int `json:"failNotifyAfter"`
	// StatusPort är porten för statussidan (/status); 0 = av.
	StatusPort int `json:"statusPort"`
}

// Error är alla fel i konfigurationen.
type Error struct{ Problems []string }

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("Fel i konfigurationen:")
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p)
	}
	return b.String()
}

// shared är fält som kan sättas både globalt och per flöde.
var shared = []string{"MASTODON_URL", "INTERVAL", "VISIBILITY", "TEMPLATE", "QUOTE_POLICY", "MAX_AGE", "MAX_POSTS_PER_POLL", "BACKFILL"}

// own är fält som bara finns per flöde. LANGUAGE hör hit eftersom en global LANGUAGE redan är en
// vanlig systemvariabel (gettext), till exempel "en_US:en".
var own = []string{"URL", "TOKEN", "ENABLED", "LANGUAGE"}

// Namnet matchas så kort som möjligt, så att FEED_X_MASTODON_URL blir flödet X och fältet MASTODON_URL.
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

// number är Number(digits) i JavaScript, utan att svämma över.
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

// ParseDuration tolkar "900" (sekunder), "15m", "1h30m" eller "2d" till millisekunder. ok är
// false om texten inte går att tolka.
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

// orderedSet är en mängd som minns ordningen, som Set i JavaScript.
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

// Load läser konfigurationen. environ är miljön i den ordning processen fick den ("NAMN=värde",
// som os.Environ), eftersom felen rapporteras i den ordningen.
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
			continue // som process.env: första förekomsten gäller
		}
		env[key] = raw
		if !strings.HasPrefix(key, "FEED_") {
			continue
		}
		m := feedKey.FindStringSubmatch(key)
		if m == nil {
			problems.add("Okänd inställning " + key + " (stavfel?)")
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
			problems.add(prefix + `ENABLED måste vara true eller false, inte "` + enabled + `"`)
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
		// pick ger flödets eget värde, annars det globala, och variabeln som gav värdet.
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
			fail(prefix + "URL saknas")
		} else if httpURL(url) == nil {
			fail(prefix + `URL är ingen http(s)-adress: "` + url + `"`)
		}
		token := fields["TOKEN"]
		if token == "" {
			fail(prefix + "TOKEN saknas")
		}

		instance, instanceSet, instanceSource := pick("MASTODON_URL")
		var instanceURL *weburl.URL
		if instanceSet {
			instanceURL = httpURL(instance)
		}
		if !instanceSet {
			fail("MASTODON_URL saknas (globalt eller som " + prefix + "MASTODON_URL)")
		} else if instanceURL == nil || instanceURL.Pathname() != "/" || instanceURL.Search() != "" {
			fail(instanceSource + " ska vara instansens adress, till exempel https://mastodon.nu")
		}

		duration := func(field, fallback string, min int64) int64 {
			value, set, source := pick(field)
			if !set {
				value = fallback
			}
			ms, ok := ParseDuration(value)
			if !ok {
				fail(source + ` är ingen tid: "` + value + `" (till exempel 900, 15m eller 1h30m)`)
			} else if ms < min {
				fail(source + " är för kort: minst " + strconv.FormatInt(min/1000, 10) + " s")
			}
			return ms
		}
		integer := func(field, fallback string, min float64) int {
			value, set, source := pick(field)
			if !set {
				value = fallback
			}
			if !digitsRE.MatchString(value) || number(value) < min {
				fail(source + " måste vara ett heltal, minst " + js.FormatNumber(min) + `: "` + value + `"`)
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
				fail(source + " måste vara " + strings.Join(allowed, ", ") + `: "` + value + `"`)
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
				fail(prefix + `LANGUAGE ska vara en språkkod som sv eller en: "` + lang + `"`)
			}
		}

		tpl, tplSet, tplSource := pick("TEMPLATE")
		if !tplSet {
			tpl = "{title}\n\n{link}"
		}
		// Ett \n i värdet blir en radbrytning, så att mallen går att skriva på en rad i .env.
		template := strings.ReplaceAll(tpl, `\n`, "\n")
		for _, m := range fieldRE.FindAllStringSubmatch(template, -1) {
			if !contains(TemplateFields, m[1]) {
				valid := make([]string, len(TemplateFields))
				for i, f := range TemplateFields {
					valid[i] = "{" + f + "}"
				}
				fail(tplSource + " har okänt fält {" + m[1] + "}; giltiga är " + strings.Join(valid, ", "))
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
			problems.add(other + " och " + feed.Prefix + " postar samma flöde till samma konto")
		} else {
			targets[target] = feed.Prefix
		}
	}

	if len(cfg.Feeds) == 0 && len(cfg.Paused) == 0 && len(problems.items) == 0 {
		problems.add("Inga flöden: ange minst FEED_<NAMN>_URL och FEED_<NAMN>_TOKEN")
	}

	numberEnv := func(name, fallback string, min, max float64) int {
		value, _ := lookup(name)
		value = js.Trim(value)
		if value == "" {
			value = fallback
		}
		n := number(value)
		if !digitsRE.MatchString(value) || n < min || n > max {
			problems.add(name + " måste vara ett heltal mellan " + js.FormatNumber(min) + " och " + js.FormatNumber(max) + `: "` + value + `"`)
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
		problems.add(`SIGNAL_API_URL är ingen http(s)-adress: "` + cfg.SignalURL + `"`)
	}
	if (cfg.SignalURL != "") != (cfg.SignalNumber != "") {
		problems.add("SIGNAL_API_URL och SIGNAL_NUMBER behövs båda för Signal-aviseringar")
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
