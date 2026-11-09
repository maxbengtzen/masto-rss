// Package poll kör ett varv för ett flöde: hämta, tolka och avgör vad som är nytt och vad som ska
// postas.
//
// Skydden mot flod och dubbletter:
//   - En post räknas som sedd om någon av dess nycklar (GUID eller normaliserad länk) har setts.
//   - Nytt flöde: allt som finns markeras som sett, och bara de `backfill` senaste postas.
//   - Ser mer än hälften av ett flöde med minst tio poster nytt ut på en gång, utan att vara nyare
//     än det senaste som setts i flödet, är något fel (till exempel nya länkar och GUID för allt):
//     allt markeras som sett och inget postas. Många nya poster med senare datum, som när en sajt
//     publicerar en hel morgons artiklar på en gång, är inget fel.
//   - Poster äldre än `maxAgeMs` postas inte.
//   - Högst `maxPostsPerPoll` per varv, äldst först. Resten väntar till nästa varv.
package poll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"sort"

	"github.com/maxbengtzen/masto-rss/internal/config"
	"github.com/maxbengtzen/masto-rss/internal/feed"
	"github.com/maxbengtzen/masto-rss/internal/format"
	"github.com/maxbengtzen/masto-rss/internal/store"
)

const (
	// ForgetAfterMs: nycklar som inte funnits i flödet så här länge glöms bort.
	ForgetAfterMs = 30 * 24 * 3600 * 1000
	// KeepPostsMs: skickade och misslyckade inlägg sparas så här länge i utkorgen.
	KeepPostsMs     = 90 * 24 * 3600 * 1000
	AnomalyMinItems = 10
)

// PostID är inläggets id, som också är dess Idempotency-Key: samma flöde och post ger alltid samma
// id, så att ett omförsök aldrig blir ett nytt inlägg och samma post inte kan hamna i utkorgen två
// gånger.
func PostID(feedName, key string) string {
	sum := sha256.Sum256([]byte("masto-rss\n" + feedName + "\n" + key))
	return hex.EncodeToString(sum[:])[:32]
}

// Decision är vad som görs med flödets nya poster.
type Decision struct {
	// Post är det som ska postas, äldst först.
	Post []*feed.Item
	// Skipped är nya poster som markeras som sedda utan att postas.
	Skipped []*feed.Item
	// Deferred är nya poster som väntar till nästa varv.
	Deferred []*feed.Item
	// Reason är varför allt nytt hoppades över, om det gjordes: "first-run" eller "anomaly".
	Reason string
}

// Options är det ur flödets inställningar som avgör.
type Options struct {
	MaxAgeMs        int64
	MaxPostsPerPoll int
	Backfill        int
}

// oldestFirst är postningsordningen: daterade poster äldst först, sedan odaterade i omvänd
// flödesordning.
func oldestFirst(items []*feed.Item) []*feed.Item {
	var dated, undated []*feed.Item
	for _, i := range items {
		if i.Published != nil {
			dated = append(dated, i)
		}
	}
	sort.SliceStable(dated, func(a, b int) bool { return *dated[a].Published < *dated[b].Published })
	for k := len(items) - 1; k >= 0; k-- {
		if items[k].Published == nil {
			undated = append(undated, items[k])
		}
	}
	return append(dated, undated...)
}

// Decide avgör vad som ska postas. seen[i] gäller items[i]. newestSeen är senaste
// publiceringsdatumet som setts i flödet förut, om det är känt.
func Decide(items []*feed.Item, seen []bool, firstRun bool, opts Options, now int64, newestSeen *int64) Decision {
	var fresh []*feed.Item
	for i, item := range items {
		if !seen[i] {
			fresh = append(fresh, item)
		}
	}
	if firstRun {
		ordered := oldestFirst(fresh)
		keep := min(opts.Backfill, len(ordered))
		return Decision{Post: ordered[len(ordered)-keep:], Skipped: ordered[:len(ordered)-keep], Reason: "first-run"}
	}
	// Bara nya poster som inte är nyare än det som redan setts kan vara gamla poster med nya nycklar.
	suspect := 0
	for _, i := range fresh {
		if newestSeen == nil || i.Published == nil || *i.Published <= *newestSeen {
			suspect++
		}
	}
	if len(items) >= AnomalyMinItems && float64(suspect) > float64(len(items))/2 {
		return Decision{Skipped: fresh, Reason: "anomaly"}
	}
	cutoff := int64(math.MinInt64)
	if opts.MaxAgeMs != 0 {
		cutoff = now - opts.MaxAgeMs
	}
	tooOld := func(i *feed.Item) bool { return i.Published != nil && *i.Published < cutoff }
	var young, old []*feed.Item
	for _, i := range fresh {
		if tooOld(i) {
			old = append(old, i)
		} else {
			young = append(young, i)
		}
	}
	ordered := oldestFirst(young)
	n := min(opts.MaxPostsPerPoll, len(ordered))
	return Decision{Post: ordered[:n], Skipped: old, Deferred: ordered[n:]}
}

// unique gör att samma post två gånger i ett flöde räknas en gång.
func unique(items []*feed.Item) []*feed.Item {
	keys := map[string]bool{}
	var out []*feed.Item
	for _, item := range items {
		dup := false
		for _, k := range item.Keys {
			if keys[k] {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		for _, k := range item.Keys {
			keys[k] = true
		}
		out = append(out, item)
	}
	return out
}

// Queued är ett inlägg som lades i utkorgen (i torrläge: som skulle ha lagts där).
type Queued struct {
	Title string
	Link  string
	// Status är inläggets text, som den kommer att postas.
	Status string
}

// Result är ett varv. Unchanged betyder 304; då är bara Redirect och PreviousFailures satta.
type Result struct {
	Unchanged bool
	Redirect  *feed.Redirect
	// PreviousFailures är misslyckade varv i rad före det här.
	PreviousFailures int
	Queued           []Queued
	Title            string
	Language         string
	// Items är posterna i flödet som går att känna igen.
	Items int
	// Unrecognized är poster utan både id och länk, som inte går att känna igen.
	Unrecognized int
	Decision     Decision
}

// Error är ett misslyckat varv.
type Error struct {
	// Failures är misslyckade varv i rad, inklusive det här.
	Failures int
	Err      error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// PollOptions är inställningarna för ett varv.
type PollOptions struct {
	// DryRun: spara ingenting.
	DryRun bool
	// Limits är instansens gränser för inläggens längd.
	Limits format.Limits
	Now    int64
	HTTP   *http.Client
}

// Poll kör ett varv för flödet.
func Poll(ctx context.Context, f config.Feed, st *store.Store, opts PollOptions) (*Result, error) {
	state, err := st.GetFeed(f.Name)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	client := opts.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	// ETag, Last-Modified och senaste datum gäller bara adressen de kom från.
	sameURL := state != nil && state.URL == f.URL
	var validators feed.Validators
	var newestSeen *int64
	if sameURL {
		validators = feed.Validators{ETag: state.ETag, LastModified: state.LastModified}
		newestSeen = state.NewestPublished
	}
	base := store.FeedState{Name: f.Name, URL: f.URL, NewestPublished: newestSeen, ETag: validators.ETag, LastModified: validators.LastModified}
	previous := 0
	if state != nil {
		base.PrimedAt = state.PrimedAt
		previous = state.Failures
	}

	fetched, err := feed.Fetch(ctx, client, f.URL, validators)
	var parsed *feed.Parsed
	if err == nil && !fetched.NotModified {
		parsed, err = feed.Parse(fetched.Text, fetched.URL)
	}
	if err != nil {
		failures := previous + 1
		next := base
		next.CheckedAt, next.Failures = &now, failures
		if state != nil {
			next.OkAt = state.OkAt
		}
		if serr := st.Transaction(opts.DryRun, func(tx *store.Store) error { return tx.SaveFeed(next) }); serr != nil {
			return nil, serr
		}
		return nil, &Error{Failures: failures, Err: err}
	}

	if fetched.NotModified {
		next := base
		next.CheckedAt, next.OkAt, next.Failures = &now, &now, 0
		if err := st.Transaction(opts.DryRun, func(tx *store.Store) error { return tx.SaveFeed(next) }); err != nil {
			return nil, err
		}
		return &Result{Unchanged: true, Redirect: fetched.Redirect, PreviousFailures: previous}, nil
	}

	var result *Result
	err = st.Transaction(opts.DryRun, func(tx *store.Store) error {
		items := unique(parsed.Items)
		seen := make([]bool, len(items))
		for i, item := range items {
			ok, err := tx.IsSeen(f.Name, item.Keys)
			if err != nil {
				return err
			}
			seen[i] = ok
		}
		decision := Decide(items, seen, state == nil || state.PrimedAt == nil,
			Options{MaxAgeMs: f.MaxAgeMs, MaxPostsPerPoll: f.MaxPostsPerPoll, Backfill: f.Backfill}, now, newestSeen)

		// Det som ska postas läggs i utkorgen i samma transaktion som det markeras som sett.
		language := f.Language
		if language == "" {
			language = parsed.Language
		}
		var queued []Queued
		for _, item := range decision.Post {
			status := format.Render(f.Template, format.Fields{
				Title: item.Title, Link: item.Link, Summary: item.Summary, Author: item.Author, Feed: parsed.Title,
			}, opts.Limits)
			post := store.Post{ID: PostID(f.Name, item.Keys[0]), Feed: f.Name, Title: item.Title, Link: item.Link, Status: status, Language: language}
			added, err := tx.Enqueue(post, now)
			if err != nil {
				return err
			}
			if added {
				queued = append(queued, Queued{Title: item.Title, Link: item.Link, Status: status})
			}
		}

		deferred := map[*feed.Item]bool{}
		for _, item := range decision.Deferred {
			deferred[item] = true
		}
		for _, item := range items {
			if !deferred[item] {
				if err := tx.MarkSeen(f.Name, item.Keys, now); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Prune(f.Name, now-ForgetAfterMs); err != nil {
			return err
		}
		if _, err := tx.PrunePosts(now - KeepPostsMs); err != nil {
			return err
		}

		// Väntande poster räknas inte förrän de tagits. Ett datum i framtiden räknas som nu, så att
		// det inte får spärren att se allt nytt som gammalt.
		newest := newestSeen
		for _, item := range items {
			if deferred[item] || item.Published == nil {
				continue
			}
			d := min(*item.Published, now)
			if newest == nil || d > *newest {
				newest = &d
			}
		}

		next := store.FeedState{
			Name: f.Name, URL: f.URL, NewestPublished: newest,
			PrimedAt: base.PrimedAt, CheckedAt: &now, OkAt: &now, Failures: 0,
		}
		if next.PrimedAt == nil {
			next.PrimedAt = &now
		}
		// Väntar poster får nästa varv inte svara 304, då skulle de bli kvar tills flödet ändras.
		if len(deferred) == 0 {
			next.ETag, next.LastModified = fetched.ETag, fetched.LastModified
		}
		if err := tx.SaveFeed(next); err != nil {
			return err
		}
		result = &Result{
			Redirect: fetched.Redirect, PreviousFailures: previous, Queued: queued,
			Title: parsed.Title, Language: parsed.Language, Items: len(items), Unrecognized: parsed.Skipped, Decision: decision,
		}
		return nil
	})
	return result, err
}
