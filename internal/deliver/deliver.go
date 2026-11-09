// Package deliver tömmer utkorgen för ett flöde: skickar väntande inlägg äldst först, ett i taget.
//
//   - Lyckat: inlägget markeras som postat, med sin adress.
//   - Mastodon nekar för gott (till exempel 422): misslyckat, och nästa inlägg tas.
//   - Token nekas (401/403): inget mer skickas för kontot, och inlägget väntar kvar.
//   - Tillfälligt fel: inlägget väntar och provas igen, efter 1, 5, 15, 60 och sedan 360 minuter.
//     Vid 429 väntar det tills Mastodons fönster öppnar igen.
//   - Har ett inlägg väntat längre än Mastodon minns Idempotency-Key (en timme), kontrolleras
//     kontots senaste inlägg först, så att ett inlägg som faktiskt kom fram inte postas två gånger.
//   - Har det väntat längre än MAX_AGE (till exempel medan en token var trasig) skickas det inte alls.
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

// IdempotencyWindowMs är hur länge Mastodon minns en Idempotency-Key.
const IdempotencyWindowMs = 3600_000

var retryMinutes = []int64{1, 5, 15, 60, 360}

// spacing är pausen mellan två inlägg till samma konto, så att de kommer i ordning och inte i en klump.
const spacing = 2 * time.Second

// Account är det som behövs om kontot.
type Account struct {
	// ID är kontots id. Behövs för kontrollen av ett inlägg som väntat längre än en timme.
	ID string
	// Quotes är sant om instansen kan ta emot citatinställningen (API-version 7 eller senare).
	Quotes bool
}

// Posted är ett skickat inlägg.
type Posted struct {
	Post         *store.Post
	URL          string
	AlreadyThere bool
}

// Failed är ett inlägg som Mastodon nekade.
type Failed struct {
	Post  *store.Post
	Error string
}

// Retry är ett tillfälligt fel som stoppade utskicket.
type Retry struct {
	Post          *store.Post
	Error         string
	NextAttemptAt int64
}

// Result är vad som hände.
type Result struct {
	Posted []Posted
	Failed []Failed
	// Expired är inlägg som väntat längre än MAX_AGE och därför aldrig skickades.
	Expired []*store.Post
	// Retry: ett tillfälligt fel stoppade utskicket; resten väntar till nästa försök.
	Retry *Retry
	// AuthError: token nekas. Inget mer skickas för kontot.
	AuthError *mastodon.Error
}

// Options är klockan och pausen, utbytbara i testerna.
type Options struct {
	Now   func() int64
	Sleep func(time.Duration)
}

var hrefRE = regexp.MustCompile(`href="([^"]+)"`)

// findExisting ger adressen till ett av kontots senaste inlägg som länkar till samma sak, om något gör det.
func findExisting(ctx context.Context, client *mastodon.Client, f config.Feed, accountID, link string) string {
	wanted := feed.NormalizeLink(link)
	statuses, err := client.RecentStatuses(ctx, f.MastodonURL, f.Token, accountID)
	if err != nil {
		return "" // går kontrollen inte att göra postas inlägget som vanligt
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

// Deliver skickar det som väntar i flödets utkorg.
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
			if err := st.MarkFailed(post.ID, "väntade längre än MAX_AGE"); err != nil {
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
