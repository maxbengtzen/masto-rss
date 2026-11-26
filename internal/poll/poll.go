// Package poll runs one poll of a feed: fetch, parse and decide what is new and what should be
// posted.
//
// The guards against floods and duplicates:
//   - An item counts as seen if any of its keys (GUID or normalized link) has been seen.
//   - New feed: everything present is marked as seen, and only the latest `backfill` are posted.
//   - If more than half of a feed with at least ten items looks new at once, without being newer
//     than the latest seen in the feed, something is wrong (for example new links and GUIDs for
//     everything): everything is marked as seen and nothing is posted. Many new items with later
//     dates, as when a site publishes a whole morning's articles at once, is not a problem.
//   - Items older than `maxAgeMs` are not posted.
//   - At most `maxPostsPerPoll` per poll, oldest first. The rest wait for the next poll.
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
	// ForgetAfterMs: keys that have not been in the feed for this long are forgotten.
	ForgetAfterMs = 30 * 24 * 3600 * 1000
	// KeepPostsMs: sent and failed posts are kept in the outbox for this long.
	KeepPostsMs     = 90 * 24 * 3600 * 1000
	AnomalyMinItems = 10
)

// PostID is the post's id, which is also its Idempotency-Key: the same feed and item always give the
// same id, so a retry never becomes a new post and the same item cannot end up in the outbox twice.
func PostID(feedName, key string) string {
	sum := sha256.Sum256([]byte("masto-rss\n" + feedName + "\n" + key))
	return hex.EncodeToString(sum[:])[:32]
}

// Decision is what is done with the feed's new items.
type Decision struct {
	// Post is what should be posted, oldest first.
	Post []*feed.Item
	// Skipped are new items that are marked as seen without being posted.
	Skipped []*feed.Item
	// Deferred are new items that wait for the next poll.
	Deferred []*feed.Item
	// Reason is why everything new was skipped, if it was: "first-run" or "anomaly".
	Reason string
}

// Options are the feed settings that the decision depends on.
type Options struct {
	MaxAgeMs        int64
	MaxPostsPerPoll int
	Backfill        int
}

// oldestFirst is the posting order: dated items oldest first, then undated ones in reverse feed
// order.
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

// Decide decides what should be posted. seen[i] applies to items[i]. newestSeen is the latest
// publication date previously seen in the feed, if known.
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
	// Only new items that are not newer than what has already been seen can be old items with new keys.
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

// unique makes the same item appearing twice in a feed count once.
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

// Queued is a post that was added to the outbox (in dry run: that would have been added).
type Queued struct {
	Title string
	Link  string
	// Status is the post's text, as it will be posted.
	Status string
}

// Result is one poll. Unchanged means 304; then only Redirect and PreviousFailures are set.
type Result struct {
	Unchanged bool
	Redirect  *feed.Redirect
	// PreviousFailures is the number of failed polls in a row before this one.
	PreviousFailures int
	Queued           []Queued
	Title            string
	Language         string
	// Items is the number of items in the feed that can be recognized.
	Items int
	// Unrecognized is the number of items with neither id nor link, which cannot be recognized.
	Unrecognized int
	Decision     Decision
}

// Error is a failed poll.
type Error struct {
	// Failures is the number of failed polls in a row, including this one.
	Failures int
	Err      error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// PollOptions are the settings for one poll.
type PollOptions struct {
	// DryRun: save nothing.
	DryRun bool
	// Limits are the instance's limits on post length.
	Limits format.Limits
	Now    int64
	HTTP   *http.Client
}

// Poll runs one poll of the feed.
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
	// ETag, Last-Modified and the latest date only apply to the URL they came from.
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

		// What should be posted is added to the outbox in the same transaction as it is marked as seen.
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

		// Deferred items do not count until they have been taken. A date in the future counts as now,
		// so it does not make the anomaly guard see everything new as old.
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
		// If items are deferred, the next poll must not get a 304, or they would stay until the feed changes.
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
