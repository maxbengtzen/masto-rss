# masto-rss

Reads RSS, Atom, RDF and JSON feeds and posts new items to Mastodon. All feeds and accounts run in a
single container. Written in Go, with no dependencies beyond the standard library and SQLite
([modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite), no cgo) for what has already been
seen and posted. The image is 13 MB, and the service uses around 13 MiB of memory with five feeds.

Up to v1.0.3 it was written in TypeScript. The Go version does the same thing, with the same
settings and the same database.

Inspired by [aserper/masto-rss](https://github.com/aserper/masto-rss), but written from scratch.
What's different: several feeds and accounts in one process, duplicate detection that survives
changed links, protection against a feed being reposted in one burst, and an outbox with
`Idempotency-Key`, so a retry never becomes a duplicate.

## Configuration

Each feed gets a name, and its settings are called `FEED_<NAME>_<FIELD>`. Global values apply to
all feeds; the same name with the prefix `FEED_<NAME>_` applies to that feed only.

```yaml
environment:
  TZ: Europe/Stockholm
  MASTODON_URL: https://mastodon.example
  FEED_NEWS_URL: https://news.example.com/rss/
  FEED_NEWS_TOKEN: ${NEWS_TOKEN}
  FEED_BLOG_URL: https://blog.example.com/feed/
  FEED_BLOG_TOKEN: ${BLOG_TOKEN}
  FEED_BLOG_INTERVAL: 30m
```

| Variable | Default | |
|---|---|---|
| `FEED_<NAME>_URL` | – | The feed's address (required) |
| `FEED_<NAME>_TOKEN` | – | Access token for the account (required), with `write:statuses` |
| `FEED_<NAME>_ENABLED` | `true` | `false` pauses the feed but keeps its history |
| `FEED_<NAME>_LANGUAGE` | from the feed | ISO 639, for example `en`. Per feed only, since `LANGUAGE` is already a system variable. Without a value, the feed's `<language>` is used |
| `MASTODON_URL` | – | The instance (required, globally or per feed) |
| `INTERVAL` | `15m` | Time between fetches: seconds or for example `15m`, `1h30m`. At least 60 s |
| `VISIBILITY` | `public` | `public`, `unlisted`, `private` or `direct` |
| `TEMPLATE` | `{title}\n\n{link}` | Fields: `{title}`, `{link}`, `{summary}`, `{author}`, `{feed}` |
| `QUOTE_POLICY` | the account's default | Who may quote: `public`, `followers` or `nobody` |
| `MAX_AGE` | `48h` | Older items are not posted. `0` turns the limit off |
| `MAX_POSTS_PER_POLL` | `5` | At most this many per fetch; the rest go out on the next round |
| `BACKFILL` | `0` | Number of items to post the first time a feed runs, regardless of `MAX_AGE` |
| `SIGNAL_API_URL`, `SIGNAL_NUMBER` | empty | Notifications via [signal-cli-rest-api](https://github.com/bbernhard/signal-cli-rest-api); off if empty |
| `FAIL_NOTIFY_AFTER` | `3` | Notify when a feed has failed this many times in a row |
| `STATUS_PORT` | `8080` | Port for the status page; `0` turns it off |
| `DATA_DIR` | `/data` | Where the database `masto-rss.db` lives |
| `DRY_RUN` | – | `1` = show what would be posted; nothing is posted or saved |
| `ONCE` | – | `1` = one round, then exit |
| `LOG_LEVEL` | `info` | `debug` also shows unchanged feeds |

An unknown `FEED_*` variable, for example a typo, stops startup with an error message.

### Safeguards

- **New feed** (or new database): everything already in the feed is marked as seen and not posted,
  except the `BACKFILL` most recent.
- **Duplicates:** an item counts as seen if its GUID or its link (without `utm_*`, `fbclid`,
  `www.`, fragment and trailing `/`) has been seen before. Every post is sent with an
  `Idempotency-Key`, and if a post has waited more than an hour for a retry, the account's recent
  posts are checked first.
- **Old items:** items older than `MAX_AGE` are not posted.
- **Anomaly:** if more than half of a feed with at least ten items looks new at once without being
  newer than the latest item seen in the feed (for example because the site changed its URLs or
  GUIDs), everything is marked as seen and nothing is posted. New items with later dates are posted
  as usual, even a whole page at once.
- **Cap:** at most `MAX_POSTS_PER_POLL` per fetch, oldest first.
- **Mentions:** `@name` in text from the feed never becomes a mention.

### Notifications

With `SIGNAL_API_URL` and `SIGNAL_NUMBER`, a Signal message is sent to the number itself when:

- a feed has failed `FAIL_NOTIFY_AFTER` times in a row, and when it works again
- Mastodon rejects a post (for example, too long) or a token (the account is paused)
- more than half of a feed looks new at once without being newer than what has been seen
- the instance changes its Mastodon version or API version, or responds with a `Deprecation` header

## Status page, Homepage and dashboards

`GET /status` on `STATUS_PORT` returns JSON with each feed's account, followers, latest post and
errors. A combined widget in [Homepage](https://gethomepage.dev):

```yaml
labels:
  homepage.name: masto-rss
  homepage.widgets[0].type: customapi
  homepage.widgets[0].url: http://masto-rss:8080/status
  homepage.widgets[0].refreshInterval: 60000
  homepage.widgets[0].display: dynamic-list
  homepage.widgets[0].mappings.items: feeds
  homepage.widgets[0].mappings.name: account
  homepage.widgets[0].mappings.label: summary
  homepage.widgets[0].mappings.target: "{url}"
```

`GET /api/dashboard` on the same port returns a short summary for a dashboard:

```json
{
  "metrics": [{"k": "accounts", "v": 5}, {"k": "last 24 h", "v": 12}, {"k": "errors", "v": 1}],
  "items": [{"name": "@news", "label": "error: feed not responding (3 in a row)", "href": "https://…"}],
  "status": "warn",
  "message": "@news: feed not responding (3 in a row)"
}
```

`items` has one row per feed (at most eight, failing ones first) with a link to the account.
`status` is `ok`, or `warn` with a `message` when a feed is failing, the same way as `errors` in
`/status`.

## Running in Docker

The image is based on `scratch`, runs as 1000:1000 and works with a read-only file system. The
database lives in `/data`, and the main loop writes a health file in `/tmp` that the health check
(`masto-rss healthcheck`) reads:

```yaml
services:
  masto-rss:
    image: ghcr.io/maxbengtzen/masto-rss:1
    read_only: true
    tmpfs: [/tmp]
    volumes:
      - ./data:/data
    environment:
      # … as above
```

## Commands

```sh
masto-rss                                             # run until SIGTERM (ONCE=1: one round)
masto-rss import <name> <file>                        # import processed_entries.txt from the old masto-rss
masto-rss post [--language en] <name,…|all> <text>    # post free text to the accounts
masto-rss healthcheck                                 # the health check: succeeds if the loop is running
```

In a running container: `docker exec masto-rss /masto-rss post all "Hello!"`.

The import marks the links in the old bot's `processed_entries.txt` as seen, so nothing is
reposted after switching. In `post`, `\n` becomes a line break.

## Mastodon compatibility

Last tested against Mastodon 4.7.3 (API version 11). The API changes in the 5.0.0-rc.1 changelog
don't affect any of the endpoints used:

| Endpoint | Used for |
|---|---|
| `GET /api/v2/instance` | Length limits, version and `api_versions` (no token) |
| `GET /api/v1/apps/verify_credentials` | That the token works and has `write:statuses` |
| `GET /api/v1/accounts/verify_credentials` | The account, followers and latest post (status page) |
| `POST /api/v1/statuses` | Posting, with `Idempotency-Key`, `visibility`, `language` and, from API version 7, `quote_approval_policy` |
| `GET /api/v1/accounts/:id/statuses` | Checking a post that has waited more than an hour |

Other servers implementing Mastodon's API (for example GoToSocial) should work. If the instance has
no `api_versions`, links are counted at full length, to be safe.

## Development

```sh
go vet ./...
go test ./...
```

Without Go installed, run it in the Go image:
`docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine go test ./...`.

More on testing, dry runs and releases in [AGENTS.md](AGENTS.md).

## License

[AGPL-3.0](LICENSE). If you run a modified version as a service for others, its source code must
be available to them.
