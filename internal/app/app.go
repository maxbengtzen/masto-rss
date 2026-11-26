// Package app is the program: it reads the configuration, opens the database, checks instances and
// tokens, and runs each feed on its own timer: fetch, add what is new to the outbox, post.
//
//	masto-rss                                   runs until SIGTERM (ONCE=1: one round and exit)
//	masto-rss import <name> <file>              imports the old bot's processed_entries.txt
//	masto-rss post [--language en] <name,…|all> <text>
//	                                            posts a free text to the feeds' accounts
//
// Feeds run one at a time, which keeps peak memory low. After a failed round the wait grows (the
// interval times 2, 4, 8 … up to 6 h, or longer if the server asked for it).
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	mastorss "github.com/maxbengtzen/masto-rss"
	"github.com/maxbengtzen/masto-rss/internal/config"
	"github.com/maxbengtzen/masto-rss/internal/deliver"
	"github.com/maxbengtzen/masto-rss/internal/feed"
	"github.com/maxbengtzen/masto-rss/internal/format"
	"github.com/maxbengtzen/masto-rss/internal/js"
	"github.com/maxbengtzen/masto-rss/internal/logx"
	"github.com/maxbengtzen/masto-rss/internal/mastodon"
	"github.com/maxbengtzen/masto-rss/internal/netx"
	"github.com/maxbengtzen/masto-rss/internal/notify"
	"github.com/maxbengtzen/masto-rss/internal/poll"
	"github.com/maxbengtzen/masto-rss/internal/status"
	"github.com/maxbengtzen/masto-rss/internal/store"
	"github.com/maxbengtzen/masto-rss/internal/weburl"
)

// HeartbeatFile is written every minute. The health check (masto-rss healthcheck) reads its time.
const HeartbeatFile = "/tmp/masto-rss.alive"

const (
	heartbeatInterval = time.Minute
	maxBackoffMs      = 6 * 3600_000
	accountRefresh    = time.Hour
	instanceRefresh   = 24 * time.Hour
	shutdownGrace     = 10 * time.Second
	usageCommands     = "masto-rss [import <name> <file> | post <name,…|all> <text>]"
)

var defaultLimits = format.Limits{MaxCharacters: 500, CharactersPerURL: 23}

// Options holds what the tests can replace. The zero value is the normal setup.
type Options struct {
	HTTP          *http.Client
	HeartbeatFile string
	// StartDelay is the wait before a feed's first round; normally i·3 s plus up to 2 s.
	StartDelay func(i int) time.Duration
	// Sleep is the pause between two posts to the same account.
	Sleep func(time.Duration)
	// Signals replaces SIGTERM and SIGINT.
	Signals <-chan os.Signal
	// Listening is called with the status page's address once it is listening.
	Listening func(addr string)
}

// account is an instance and a token. Several feeds can share an account.
type account struct {
	instance string
	token    string
	feeds    []config.Feed
	info     *mastodon.Account
	// blocked is why the account cannot post, if it cannot.
	blocked string
}

type instance struct {
	limits     format.Limits
	apiVersion int
}

type app struct {
	cfg    *config.Config
	store  *store.Store
	log    *logx.Logger
	notify *notify.Notifier
	client *mastodon.Client
	http   *http.Client
	opts   Options

	mu              sync.Mutex // guards the accounts and the instances
	accounts        []*account
	accountByKey    map[string]*account
	instances       map[string]instance
	instanceOrder   []string
	redirectsLogged map[string]bool
}

func accountKey(f config.Feed) string { return f.MastodonURL + "\n" + f.Token }

func host(u string) string {
	parsed, err := weburl.Parse(u, nil)
	if err != nil {
		return u
	}
	return parsed.HostPort()
}

func oneLine(text string) string { return strings.ReplaceAll(text, "\n", " ⏎ ") }

func duration(ms int64) string {
	switch {
	case ms%3_600_000 == 0:
		return strconv.FormatInt(ms/3_600_000, 10) + " h"
	case ms%60_000 == 0:
		return strconv.FormatInt(ms/60_000, 10) + " min"
	}
	return strconv.FormatInt(int64(math.Round(float64(ms)/1000)), 10) + " s"
}

func nowMs() int64 { return time.Now().UnixMilli() }

// Main runs the program and returns the exit code.
func Main(args, environ []string, stdout, stderr io.Writer, opts Options) int {
	exit := func(msg string) int {
		fmt.Fprintln(stderr, msg)
		return 1
	}
	cfg, err := config.Load(environ)
	if err != nil {
		return exit(err.Error())
	}
	log := &logx.Logger{Out: stdout, Debug: cfg.Debug}

	dbPath := filepath.Join(cfg.DataDir, "masto-rss.db")
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return exit(fmt.Sprintf("Cannot open the database %s: %s", dbPath, netx.Describe(err)))
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return exit(fmt.Sprintf("Cannot open the database %s: %s", dbPath, netx.Describe(err)))
	}
	defer st.Close()

	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	a := &app{
		cfg: cfg, store: st, log: log, opts: opts, http: httpClient,
		notify:          &notify.Notifier{URL: cfg.SignalURL, Number: cfg.SignalNumber, DryRun: cfg.DryRun, HTTP: httpClient, Log: log},
		client:          &mastodon.Client{HTTP: httpClient},
		accountByKey:    map[string]*account{},
		instances:       map[string]instance{},
		redirectsLogged: map[string]bool{},
	}
	defer a.notify.Wait()
	for _, f := range cfg.Feeds {
		acc := a.accountByKey[accountKey(f)]
		if acc == nil {
			acc = &account{instance: f.MastodonURL, token: f.Token}
			a.accountByKey[accountKey(f)] = acc
			a.accounts = append(a.accounts, acc)
		}
		acc.feeds = append(acc.feeds, f)
	}

	command := ""
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "import":
		return a.importCommand(args, stderr)
	case "post":
		return a.postCommand(args, stderr)
	case "":
	default:
		return exit(fmt.Sprintf("Unknown command \"%s\". Usage: %s", command, usageCommands))
	}
	return a.run()
}

func (a *app) feedNames() string {
	names := make([]string, len(a.cfg.Feeds))
	for i, f := range a.cfg.Feeds {
		names[i] = f.Name
	}
	return strings.Join(names, ", ")
}

func (a *app) importCommand(args []string, stderr io.Writer) int {
	var name, file string
	if len(args) > 0 {
		name = args[0]
	}
	if len(args) > 1 {
		file = args[1]
	}
	var f *config.Feed
	for i := range a.cfg.Feeds {
		if a.cfg.Feeds[i].Name == strings.ToLower(name) {
			f = &a.cfg.Feeds[i]
		}
	}
	if f == nil || file == "" {
		fmt.Fprintf(stderr, "Usage: masto-rss import <name> <file>, where name is one of: %s\n", a.feedNames())
		return 1
	}
	b, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "Cannot read %s: %s\n", file, netx.Describe(err))
		return 1
	}
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(weburl.DecodeUTF8(b), "\r\n", "\n"), "\n") {
		if l = js.Trim(l); l != "" {
			lines = append(lines, l)
		}
	}
	var keys []string
	seen := map[string]bool{}
	for _, l := range lines {
		k := "l:" + feed.NormalizeLink(l)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	var added int
	err = a.store.Transaction(a.cfg.DryRun, func(tx *store.Store) error {
		var err error
		added, err = tx.ImportKeys(f.Name, f.URL, keys, nowMs())
		return err
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", netx.Describe(err))
		return 1
	}
	verb := "imported"
	if a.cfg.DryRun {
		verb = "would import"
	}
	a.log.Printf("%s: %s %s from %s (%s)", f.Name, verb, count(len(lines), "link", "links"), file, count(added, "new key", "new keys"))
	return 0
}

func (a *app) postCommand(args []string, stderr io.Writer) int {
	language := ""
	if len(args) > 0 && args[0] == "--language" {
		if len(args) > 1 {
			language = args[1]
		}
		args = args[min(2, len(args)):]
	}
	which := ""
	if len(args) > 0 {
		which = args[0]
		args = args[1:]
	}
	text := js.Trim(strings.ReplaceAll(strings.Join(args, " "), `\n`, "\n"))
	var wanted []string
	if which == "all" || which == "alla" { // "alla" is the old Swedish keyword
		for _, f := range a.cfg.Feeds {
			wanted = append(wanted, f.Name)
		}
	} else {
		for _, n := range strings.Split(strings.ToLower(which), ",") {
			if n != "" {
				wanted = append(wanted, n)
			}
		}
	}
	known := func(n string) bool {
		for _, f := range a.cfg.Feeds {
			if f.Name == n {
				return true
			}
		}
		return false
	}
	valid := text != "" && len(wanted) > 0
	for _, n := range wanted {
		valid = valid && known(n)
	}
	if !valid {
		fmt.Fprintf(stderr, "Usage: masto-rss post [--language en] <name,…|all> <text>, where name is one of: %s\n", a.feedNames())
		return 1
	}
	ok := true
	for _, acc := range a.accounts {
		var f *config.Feed
		for i := range acc.feeds {
			for _, n := range wanted {
				if f == nil && acc.feeds[i].Name == n {
					f = &acc.feeds[i]
				}
			}
		}
		if f == nil {
			continue
		}
		if a.cfg.DryRun {
			a.log.Printf("%s: would post: %s", f.Name, oneLine(text))
			continue
		}
		// The same text to the same account gives the same key, so a repeat within an hour is not a new post.
		sum := sha256.Sum256([]byte("masto-rss post\n" + accountKey(*f) + "\n" + text))
		key := hex.EncodeToString(sum[:])[:32]
		lang := language
		if lang == "" {
			lang = f.Language
		}
		posted, err := a.client.Post(context.Background(), acc.instance, acc.token, mastodon.StatusParams{Status: text, Visibility: f.Visibility, Language: lang}, key)
		if err != nil {
			ok = false
			a.log.Printf("%s: ERROR: %s", f.Name, netx.Describe(err))
			continue
		}
		a.log.Printf("%s: posted %s", f.Name, posted.URL)
	}
	if !ok {
		return 1
	}
	return 0
}

func (a *app) run() int {
	cfg := a.cfg
	dry := ""
	if cfg.DryRun {
		dry = " in dry-run mode: nothing is posted and nothing is saved"
	}
	a.log.Printf("masto-rss %s starting%s", mastorss.Version(), dry)
	for _, f := range cfg.Feeds {
		lang := f.Language
		if lang == "" {
			lang = "from the feed"
		}
		a.log.Printf("  %s: %s → %s, every %s, %s, language %s", f.Name, f.URL, host(f.MastodonURL), duration(f.IntervalMs), f.Visibility, lang)
	}
	if len(cfg.Paused) > 0 {
		a.log.Printf("  paused: %s", strings.Join(cfg.Paused, ", "))
	}

	configured := make([]string, 0, len(cfg.Feeds)+len(cfg.Paused))
	for _, f := range cfg.Feeds {
		configured = append(configured, f.Name)
	}
	configured = append(configured, cfg.Paused...)
	orphans, err := a.store.Orphans(configured)
	if err != nil {
		a.log.Printf("%s", netx.Describe(err))
	}
	for _, o := range orphans {
		if !cfg.DryRun && nowMs()-o.LastUsed > poll.ForgetAfterMs {
			a.store.Transaction(false, func(tx *store.Store) error { return tx.Forget(o.Name) })
			a.log.Printf("%s: not in the configuration for 30 days, its history has been removed", o.Name)
		} else {
			a.log.Printf("%s: in the database but not in the configuration; its history is removed after 30 days", o.Name)
		}
	}

	a.client.SetDeprecationHandler(func(endpoint, value string) {
		message := fmt.Sprintf("Mastodon says that %s is being phased out (Deprecation: %s). masto-rss needs to be updated.", endpoint, value)
		if cfg.DryRun {
			a.log.Printf("%s", message)
		} else if first, _ := a.store.FirstNotice("deprecation "+endpoint, nowMs()); first {
			a.log.Printf("%s", message)
			a.notify.Send(message)
		}
	})

	ctx := context.Background()
	seenInstance := map[string]bool{}
	for _, f := range cfg.Feeds {
		if !seenInstance[f.MastodonURL] {
			seenInstance[f.MastodonURL] = true
			a.checkInstance(ctx, f.MastodonURL)
		}
	}
	for _, acc := range a.accounts {
		a.checkAccount(ctx, acc, true)
	}

	if cfg.Once {
		ok := true
		for _, f := range cfg.Feeds {
			ok = a.runFeed(ctx, f).ok && ok
		}
		if !ok {
			return 1
		}
		return 0
	}
	return a.serve(ctx)
}

func (a *app) checkInstance(ctx context.Context, url string) {
	info, err := a.client.Instance(ctx, url)
	if err != nil {
		a.log.Printf("%s: could not read the instance's details (%s); assuming %d characters", host(url), netx.Describe(err), defaultLimits.MaxCharacters)
		return
	}
	// Without api_versions (older Mastodon or another server) links count at full length, to be safe.
	limits := format.Limits{MaxCharacters: info.MaxCharacters}
	if info.APIVersion != 0 {
		limits.CharactersPerURL = info.CharactersPerURL
	}
	a.mu.Lock()
	if _, ok := a.instances[url]; !ok {
		a.instanceOrder = append(a.instanceOrder, url)
	}
	a.instances[url] = instance{limits: limits, apiVersion: info.APIVersion}
	a.mu.Unlock()

	before, _ := a.store.GetInstance(url)
	minor := func(v string) string {
		parts := strings.Split(v, ".")
		return strings.Join(parts[:min(2, len(parts))], ".")
	}
	switch {
	case before == nil:
		a.log.Printf("%s: Mastodon %s, API version %d, %d characters per post", host(url), info.Version, info.APIVersion, info.MaxCharacters)
	case before.APIVersion != info.APIVersion || minor(before.Version) != minor(info.Version):
		message := fmt.Sprintf("%s has gone from Mastodon %s (API %d) to %s (API %d). Read Mastodon's CHANGELOG and check whether masto-rss needs changes.",
			host(url), before.Version, before.APIVersion, info.Version, info.APIVersion)
		a.log.Printf("%s", message)
		a.notify.Send(message)
	case before.Version != info.Version:
		a.log.Printf("%s: Mastodon %s → %s", host(url), before.Version, info.Version)
	}
	if !a.cfg.DryRun {
		a.store.SaveInstance(url, info.Version, info.APIVersion, nowMs())
	}
}

func names(acc *account) string {
	n := make([]string, len(acc.feeds))
	for i, f := range acc.feeds {
		n[i] = f.Name
	}
	return strings.Join(n, ", ")
}

func (a *app) block(acc *account, reason string) {
	a.mu.Lock()
	if acc.blocked != "" {
		a.mu.Unlock()
		return
	}
	acc.blocked = reason
	a.mu.Unlock()
	message := fmt.Sprintf("%s: %s. Nothing is posted for the account until this is fixed and masto-rss is restarted.", names(acc), reason)
	a.log.Printf("%s", message)
	a.notify.Send(message)
}

func (a *app) checkAccount(ctx context.Context, acc *account, first bool) {
	if first {
		app, err := a.client.VerifyApp(ctx, acc.instance, acc.token)
		var me *mastodon.Error
		switch {
		case err == nil && !mastodon.CanPost(app.Scopes):
			has := strings.Join(app.Scopes, ", ")
			if has == "" {
				has = "none"
			}
			a.block(acc, "token lacks write:statuses (has "+has+")")
		case errors.As(err, &me) && me.Kind() == "auth":
			a.block(acc, "token rejected ("+me.Error()+")")
		case err != nil:
			a.log.Printf("%s: could not check the token (%s)", names(acc), netx.Describe(err))
		}
	}
	a.mu.Lock()
	blocked := acc.blocked != ""
	a.mu.Unlock()
	if blocked {
		return
	}
	info, err := a.client.VerifyAccount(ctx, acc.instance, acc.token)
	if err != nil {
		// The account is only needed for the status page and the hourly check, not for posting.
		if first {
			a.log.Printf("%s: could not read the account (%s)", names(acc), netx.Describe(err))
		}
		return
	}
	a.mu.Lock()
	acc.info = info
	a.mu.Unlock()
	if first {
		bot := ". The account is not marked as a bot (Preferences → Profile)"
		if info.Bot {
			bot = ""
		}
		a.log.Printf("%s: @%s@%s, %s%s", names(acc), info.Acct, host(acc.instance), count(info.Followers, "follower", "followers"), bot)
	}
}

func (a *app) report(f config.Feed, r *poll.Result) {
	name := f.Name
	if r.Redirect != nil && !a.redirectsLogged[name] {
		a.redirectsLogged[name] = true
		if r.Redirect.Permanent {
			a.log.Printf("%s: the address has moved permanently to %s; update %sURL", name, r.Redirect.To, f.Prefix)
		} else {
			a.log.Debugf("%s: temporarily redirected to %s", name, r.Redirect.To)
		}
	}
	quiet := a.log.Debugf
	if a.cfg.Once {
		quiet = a.log.Printf
	}
	if r.Unchanged {
		quiet("%s: unchanged (304)", name)
		return
	}
	d := r.Decision
	if r.Unrecognized > 0 {
		a.log.Debugf("%s: skipping %s with neither id nor link", name, count(r.Unrecognized, "item", "items"))
	}
	switch {
	case d.Reason == "first-run":
		rest := ", nothing is posted"
		if len(d.Post) > 0 {
			rest = fmt.Sprintf(", %d added to the outbox (BACKFILL)", len(d.Post))
		}
		a.log.Printf("%s: new feed, %s marked as seen%s", name, count(len(d.Skipped), "item", "items"), rest)
	case d.Reason == "anomaly":
		a.log.Printf("%s: WARNING: %d of %d items look new at once. Everything is marked as seen and nothing is posted. Has the site changed its addresses or GUIDs?", name, len(d.Skipped), r.Items)
	case len(d.Skipped) > 0:
		a.log.Printf("%s: skipping %s older than %s", name, count(len(d.Skipped), "item", "items"), duration(f.MaxAgeMs))
	}
	for _, q := range r.Queued {
		if a.cfg.DryRun {
			a.log.Printf("%s: would post: %s", name, oneLine(q.Status))
		} else {
			a.log.Debugf("%s: in the outbox: %s", name, q.Title)
		}
	}
	if len(d.Deferred) > 0 {
		a.log.Printf("%s: %d more wait until the next round (at most %d per round)", name, len(d.Deferred), f.MaxPostsPerPoll)
	}
	if len(d.Post) == 0 && len(d.Skipped) == 0 && len(d.Deferred) == 0 {
		quiet("%s: nothing new (%s)", name, count(r.Items, "item", "items"))
	}
}

// send sends what is waiting in the feed's outbox.
func (a *app) send(ctx context.Context, f config.Feed) error {
	a.mu.Lock()
	acc := a.accountByKey[accountKey(f)]
	blocked := acc.blocked != ""
	var accountID string
	if acc.info != nil {
		accountID = acc.info.ID
	}
	quotes := a.instances[f.MastodonURL].apiVersion >= 7
	a.mu.Unlock()
	if a.cfg.DryRun || blocked {
		return nil
	}
	result, err := deliver.Deliver(ctx, a.client, f, a.store, deliver.Account{ID: accountID, Quotes: quotes}, deliver.Options{Sleep: a.opts.Sleep})
	for _, p := range result.Posted {
		verb := "posted"
		if p.AlreadyThere {
			verb = "already there"
		}
		title := p.Post.Title
		if title == "" {
			title = "untitled"
		}
		a.log.Printf("%s: %s %s (%s)", f.Name, verb, p.URL, title)
	}
	for _, p := range result.Expired {
		a.log.Printf("%s: not posted, waited longer than %s: %s", f.Name, duration(f.MaxAgeMs), p.Title)
	}
	for _, fl := range result.Failed {
		a.log.Printf("%s: Mastodon rejected \"%s\": %s", f.Name, fl.Post.Title, fl.Error)
		a.notify.Send(fmt.Sprintf("Mastodon rejected a post from %s (%s): %s", f.Name, fl.Error, fl.Post.Title))
	}
	if result.Retry != nil {
		at := time.UnixMilli(result.Retry.NextAttemptAt).Format("15:04")
		a.log.Printf("%s: could not post (%s); next attempt at %s at the earliest", f.Name, result.Retry.Error, at)
	}
	if result.AuthError != nil {
		a.block(acc, "token rejected ("+result.AuthError.Error()+")")
	}
	return err
}

type outcome struct {
	ok     bool
	nextMs int64
}

// runFeed runs one round and returns the time until the next.
func (a *app) runFeed(ctx context.Context, f config.Feed) outcome {
	a.mu.Lock()
	limits := defaultLimits
	if in, ok := a.instances[f.MastodonURL]; ok {
		limits = in.limits
	}
	a.mu.Unlock()

	var out outcome
	result, err := poll.Poll(ctx, f, a.store, poll.PollOptions{DryRun: a.cfg.DryRun, Limits: limits, Now: nowMs(), HTTP: a.http})
	if err == nil {
		a.report(f, result)
		if result.PreviousFailures >= a.cfg.FailNotifyAfter {
			a.notify.Send(f.Name + ": the feed works again")
		}
		if !result.Unchanged && result.Decision.Reason == "anomaly" {
			a.notify.Send(fmt.Sprintf("%s: %d of %d items looked new at once. Everything was marked as seen and nothing was posted. Has the site changed its addresses or GUIDs?",
				f.Name, len(result.Decision.Skipped), result.Items))
		}
		out = outcome{ok: true, nextMs: int64(math.Round(float64(f.IntervalMs) * (0.9 + rand.Float64()*0.2)))}
	} else {
		failures := 1
		cause := err
		var pe *poll.Error
		if errors.As(err, &pe) {
			failures, cause = pe.Failures, pe.Err
		}
		retryAfterMs := 0.0
		if h, ok := feed.IsHTTPError(cause); ok && h.RetryAfter >= 0 {
			retryAfterMs = float64(h.RetryAfter) * 1000
		}
		next := math.Min(math.Max(float64(f.IntervalMs)*math.Pow(2, float64(failures-1)), retryAfterMs), maxBackoffMs)
		a.log.Printf("%s: ERROR (%d in a row): %s; next attempt in %d min", f.Name, failures, netx.Describe(cause), max(1, int64(math.Round(next/60_000))))
		if failures == a.cfg.FailNotifyAfter {
			a.notify.Send(fmt.Sprintf("%s: the feed could not be fetched %s in a row (%s)", f.Name, count(failures, "time", "times"), netx.Describe(cause)))
		}
		out = outcome{ok: false, nextMs: int64(next)}
	}
	// The outbox is emptied even when fetching fails: it can hold posts from earlier rounds.
	if err := a.send(ctx, f); err != nil {
		a.log.Printf("%s: error while posting: %s", f.Name, netx.Describe(err))
	}
	return out
}

func (a *app) snapshot() status.Snapshot {
	now := nowMs()
	feeds := make([]status.Feed, 0, len(a.cfg.Feeds))
	errorsCount := 0
	for _, f := range a.cfg.Feeds {
		a.mu.Lock()
		acc := a.accountByKey[accountKey(f)]
		blocked := acc.blocked
		var info *mastodon.Account
		if acc.info != nil {
			copied := *acc.info
			info = &copied
		}
		a.mu.Unlock()

		failures := 0
		if st, _ := a.store.GetFeed(f.Name); st != nil {
			failures = st.Failures
		}
		problem := blocked
		if problem == "" && failures > 0 {
			problem = fmt.Sprintf("feed not responding (%d in a row)", failures)
		}
		last, _ := a.store.LastPosted(f.Name)
		var lastText string
		switch {
		case last != nil:
			lastText = status.Ago(*last, now)
		case info != nil && info.LastStatusAt != "":
			lastText = status.DaysAgo(info.LastStatusAt, now, time.Local)
		default:
			lastText = "no posts yet"
		}
		fs := status.Feed{Name: f.Name, Account: f.Name, OK: problem == ""}
		followers := ""
		if info != nil {
			fs.Account = "@" + info.Acct
			fs.URL = info.URL
			fs.Followers, fs.Posts = &info.Followers, &info.Posts
			followers = status.Followers(info.Followers) + " · "
		}
		if last != nil {
			fs.LastPost = time.UnixMilli(*last).UTC().Format("2006-01-02T15:04:05.000Z")
		}
		if problem != "" {
			fs.Summary = "error: " + problem
			fs.Problem = problem
			errorsCount++
		} else {
			fs.Summary = followers + lastText
		}
		feeds = append(feeds, fs)
	}
	posted, _ := a.store.PostedSince(now - 86_400_000)
	return status.Snapshot{
		Version: mastorss.Version(),
		Totals:  status.Totals{Feeds: len(feeds), PostedLastDay: posted, Errors: errorsCount},
		Feeds:   feeds,
	}
}

// serve runs the feeds on their timers until SIGTERM or SIGINT.
func (a *app) serve(ctx context.Context) int {
	cfg := a.cfg
	var server *http.Server
	if cfg.StatusPort != 0 {
		listener, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.StatusPort))
		if err != nil {
			a.log.Printf("The status page could not start on port %d: %s", cfg.StatusPort, netx.Describe(err))
		} else {
			server = &http.Server{Handler: status.Handler(a.snapshot), ReadHeaderTimeout: 10 * time.Second}
			go server.Serve(listener)
			if a.opts.Listening != nil {
				a.opts.Listening(listener.Addr().String())
			}
		}
	}

	heartbeatFile := a.opts.HeartbeatFile
	if heartbeatFile == "" {
		heartbeatFile = HeartbeatFile
	}
	stop := make(chan struct{})
	var background sync.WaitGroup
	heartbeat := func() bool {
		if err := os.WriteFile(heartbeatFile, nil, 0o644); err != nil {
			a.log.Printf("Cannot write %s, the health check will fail: %s", heartbeatFile, netx.Describe(err))
			return false
		}
		return true
	}
	if heartbeat() {
		background.Add(1)
		go func() {
			defer background.Done()
			t := time.NewTicker(heartbeatInterval)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if !heartbeat() {
						return
					}
				}
			}
		}()
	}

	// Accounts are re-read every hour (for the status page) and instances every day.
	background.Add(1)
	go func() {
		defer background.Done()
		accounts := time.NewTicker(accountRefresh)
		instances := time.NewTicker(instanceRefresh)
		defer accounts.Stop()
		defer instances.Stop()
		for {
			select {
			case <-stop:
				return
			case <-accounts.C:
				for _, acc := range a.accounts {
					go a.checkAccount(ctx, acc, false)
				}
			case <-instances.C:
				a.mu.Lock()
				urls := append([]string(nil), a.instanceOrder...)
				a.mu.Unlock()
				for _, url := range urls {
					go a.checkInstance(ctx, url)
				}
			}
		}
	}()

	// Feeds run one at a time, in the order their timers fire.
	queue := make(chan config.Feed, len(cfg.Feeds))
	var timersMu sync.Mutex
	timers := map[string]*time.Timer{}
	stopping := false
	schedule := func(f config.Feed, delay time.Duration) {
		timersMu.Lock()
		defer timersMu.Unlock()
		if stopping {
			return
		}
		timers[f.Name] = time.AfterFunc(delay, func() { queue <- f })
	}
	startDelay := a.opts.StartDelay
	if startDelay == nil {
		// Spread the first fetches over a few seconds, so they don't all start at once.
		startDelay = func(i int) time.Duration {
			return time.Duration(float64(i)*3000+rand.Float64()*2000) * time.Millisecond
		}
	}
	for i, f := range cfg.Feeds {
		schedule(f, startDelay(i))
	}
	if len(cfg.Feeds) == 0 {
		a.log.Printf("All feeds are paused; waiting without doing anything")
	}

	idle := make(chan struct{})
	go func() {
		defer close(idle)
		for {
			select {
			case <-stop:
				return
			case f := <-queue:
				select {
				case <-stop:
					return
				default:
				}
				result := a.runFeed(ctx, f)
				schedule(f, time.Duration(result.nextMs)*time.Millisecond)
			}
		}
	}()

	signals := a.opts.Signals
	if signals == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		signals = ch
	}
	sig := <-signals
	name := "SIGTERM"
	if sig == os.Interrupt {
		name = "SIGINT"
	}
	a.log.Printf("%s: shutting down", name)
	timersMu.Lock()
	stopping = true
	for _, t := range timers {
		t.Stop()
	}
	timersMu.Unlock()
	close(stop)
	if server != nil {
		server.Close()
	}
	// A round in progress gets a moment to finish.
	select {
	case <-idle:
	case <-time.After(shutdownGrace):
	}
	background.Wait()
	return 0
}

// count writes n with the singular or plural noun: "1 item", "3 items".
func count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}
