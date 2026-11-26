// Package deliver empties a feed's outbox: it sends pending posts oldest first, one at a time.
//
//   - Success: the post is marked as posted, with its URL.
//   - Mastodon rejects it for good (for example 422): it fails, and the next post is taken.
//   - Token not accepted (401/403): nothing more is sent for the account, and the post stays pending.
//   - Transient error: the post waits and is retried after 1, 5, 15, 60 and then 360 minutes.
//     On 429 it waits until Mastodon's window opens again.
//   - If a post has waited longer than Mastodon remembers the Idempotency-Key (one hour), the
//     account's latest posts are checked first, so a post that actually got through is not posted twice.
//   - If it has waited longer than MAX_AGE (for example while a token was broken), it is not sent at all.
package deliver

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/maxbengtzen/masto-rss/internal/config"
	"github.com/maxbengtzen/masto-rss/internal/feed"
	"github.com/maxbengtzen/masto-rss/internal/mastodon"
	"github.com/maxbengtzen/masto-rss/internal/store"
)

// IdempotencyWindowMs is how long Mastodon remembers an Idempotency-Key.
const IdempotencyWindowMs = 3600_000

var retryMinutes = []int64{1, 5, 15, 60, 360}

// spacing is the pause between two posts to the same account, so they arrive in order and not in a burst.
const spacing = 2 * time.Second

// Account is what we need to know about the account.
type Account struct {
	// ID is the account id. Needed to check a post that has waited longer than an hour.
	ID string
	// Quotes is true if the instance accepts the quote policy setting (API version 7 or later).
	Quotes bool
}

// Posted is a sent post.
type Posted struct {
	Post         *store.Post
	URL          string
	AlreadyThere bool
}

// Failed is a post that Mastodon rejected.
type Failed struct {
	Post  *store.Post
	Error string
}

// Retry is a transient error that stopped delivery.
type Retry struct {
	Post          *store.Post
	Error         string
	NextAttemptAt int64
}

// Result is what happened.
type Result struct {
	Posted []Posted
	Failed []Failed
	// Expired are posts that waited longer than MAX_AGE and were therefore never sent.
	Expired []*store.Post
	// Retry: a transient error stopped delivery; the rest wait for the next attempt.
	Retry *Retry
	// AuthError: the token is not accepted. Nothing more is sent for the account.
	AuthError *mastodon.Error
}

// Options are the clock and the pause, replaceable in tests.
type Options struct {
	Now   func() int64
	Sleep func(time.Duration)
}

var hrefRE = regexp.MustCompile(`href="([^"]+)"`)

// findExisting returns the URL of one of the account's latest posts that links to the same thing, if any does.
func findExisting(ctx context.Context, client *mastodon.Client, f config.Feed, accountID, link string) string {
	wanted := feed.NormalizeLink(link)
	statuses, err := client.RecentStatuses(ctx, f.MastodonURL, f.Token, accountID)
	if err != nil {
		return "" // if the check cannot be made, the post is posted as usual
	}
	for _, s := range statuses {
		urls := []string{s.Card}
		for _, m := range hrefRE.FindAllStringSubmatch(s.Content, -1) {
			urls = append(urls, feed.DecodeEntities(m[1]))
		}
		for _, u := range urls {
			if u != "" && feed.NormalizeLink(u) == wanted {
				return s.URL
			}
		}
	}
	return ""
}

// Deliver sends what is waiting in the feed's outbox.
func Deliver(ctx context.Context, client *mastodon.Client, f config.Feed, st *store.Store, account Account, opts Options) (*Result, error) {
	now := opts.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	result := &Result{}
	due, err := st.DuePosts(f.Name, now())
	if err != nil {
		return result, err
	}
	for i, post := range due {
		if f.MaxAgeMs > 0 && now()-post.CreatedAt > f.MaxAgeMs {
			if err := st.MarkFailed(post.ID, "waited longer than MAX_AGE"); err != nil {
				return result, err
			}
			result.Expired = append(result.Expired, post)
			continue
		}
		if i > 0 {
			sleep(spacing)
		}
		if post.Attempts > 0 && account.ID != "" && post.Link != "" && now()-post.CreatedAt > IdempotencyWindowMs {
			if existing := findExisting(ctx, client, f, account.ID, post.Link); existing != "" {
				if err := st.MarkPosted(post.ID, existing, now()); err != nil {
					return result, err
				}
				result.Posted = append(result.Posted, Posted{Post: post, URL: existing, AlreadyThere: true})
				continue
			}
		}
		params := mastodon.StatusParams{Status: post.Status, Visibility: f.Visibility, Language: post.Language}
		if account.Quotes {
			params.QuotePolicy = f.QuotePolicy
		}
		status, err := client.Post(ctx, f.MastodonURL, f.Token, params, post.ID)
		if err == nil {
			if err := st.MarkPosted(post.ID, status.URL, now()); err != nil {
				return result, err
			}
			result.Posted = append(result.Posted, Posted{Post: post, URL: status.URL})
			continue
		}
		message := err.Error()
		var me *mastodon.Error
		isMastodon := errors.As(err, &me)
		if isMastodon && me.Kind() == "auth" {
			if err := st.MarkRetry(post.ID, now()+3600_000, message); err != nil {
				return result, err
			}
			result.AuthError = me
			break
		}
		if isMastodon && me.Kind() == "rejected" {
			if err := st.MarkFailed(post.ID, message); err != nil {
				return result, err
			}
			result.Failed = append(result.Failed, Failed{Post: post, Error: message})
			continue
		}
		var delayMs int64
		if isMastodon && me.Status == 429 && me.ResetAt != 0 {
			delayMs = max(me.ResetAt-now(), 60_000)
		} else {
			delayMs = retryMinutes[min(post.Attempts, len(retryMinutes)-1)] * 60_000
		}
		next := now() + delayMs
		if err := st.MarkRetry(post.ID, next, message); err != nil {
			return result, err
		}
		result.Retry = &Retry{Post: post, Error: message, NextAttemptAt: next}
		break
	}
	return result, nil
}
