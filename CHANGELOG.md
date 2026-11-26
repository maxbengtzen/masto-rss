# Changelog

All changes noticeable to whoever runs the service. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/). How a version is released is described in
[AGENTS.md](AGENTS.md).

## [Unreleased]

### Fixed
- Log lines and notifications use the singular for one: "1 follower", "1 item marked as seen",
  "could not be fetched 1 time in a row".

## [1.3.0] - 2026-11-26

### Changed
- All text is now in English: log lines, Signal notifications, error messages, the summaries in
  `/status` and `/api/dashboard`, the documentation and the code. Settings, templates and the
  database are unchanged, and nothing needs to change in the compose file. If you match on log text
  or on the dashboard's metric names, update those matches. Follower counts are written as
  `12,345 followers`.
- `masto-rss post` takes `all` to post to every account. The old Swedish keyword `alla` still works.
- The license is now MIT instead of AGPL-3.0. Versions up to and including 1.2.0 remain available
  under AGPL-3.0.

## [1.2.0] - 2026-11-16

### Added
- `GET /api/dashboard` on `STATUS_PORT` returns a short summary for a dashboard: number of accounts,
  posts in the last 24 hours and errors, one row per feed with a link to the account, and
  `status: warn` with a message when a feed is failing. `/status` is unchanged.

## [1.1.2] - 2026-11-13

### Security
- Built with Go 1.27.4, which fixes CVE-2026-97054 in the standard library. The Go version in the
  Dockerfile is now pinned, so Dependabot proposes new Go patch releases.

## [1.1.1] - 2026-11-10

### Fixed
- 1.1.0 was never published: the tests failed in CI on the new version number. 1.1.1 contains the
  same changes as listed under 1.1.0 below.

## [1.1.0] - 2026-11-10

### Changed
- masto-rss is rewritten in Go. It does the same as before, with the same environment variables,
  template fields, log texts, notifications and `/status`, and the existing database is used as is.
  Nothing needs to change in the compose file.
- The image is 13 MB instead of 184 MB, and the service uses around 13 MiB of memory instead of
  40–45 MiB. It is based on `scratch` and runs as user 1000:1000 (the same uid as before).
- The commands are `masto-rss import …` and `masto-rss post …`. In a running container:
  `docker exec masto-rss /masto-rss import <name> <file>`, instead of `node src/main.ts …`.
- The health check is `masto-rss healthcheck`, with the same rule as before: the health file in
  `/tmp` must be younger than three minutes. With `read_only: true`, `/tmp` still needs to be a
  tmpfs.
- The reason in parentheses in the log for network errors and broken XML is worded differently.
- Character encodings other than UTF-8, UTF-16, ISO-8859-1/windows-1252 or ISO-8859-15 are read as
  UTF-8.

### Fixed
- Notifications sent just before masto-rss exits (for example with `ONCE=1`) are delivered.

## [1.0.3] - 2026-10-29

### Changed
- The image is based on the floating tag `node:26-alpine` instead of a pinned 26.x version, so
  every release gets the latest Node 26 and Alpine. Nothing needs to change in the compose file.

## [1.0.2] - 2026-10-27

### Fixed
- The anomaly guard triggered when a site published as many new items as the feed holds at once
  (for example a whole morning's articles in a feed with ten items), and then nothing was posted.
  Now only new items that are not newer than the latest seen in the feed count as suspicious, so a
  real change of URLs or GUIDs is still stopped. The database is upgraded automatically on startup
  (schema version 3). To roll back to 1.0.1, the schema version must first be set back to 2.

## [1.0.1] - 2026-10-26

### Changed
- The source code is public and licensed under AGPL-3.0. The service works as before.

## [1.0.0] - 2026-10-26

First version. Replaces one container per feed running `ghcr.io/aserper/masto-rss` with a single
container for all feeds and accounts.

### Added
- Reads RSS, Atom, RDF and JSON feeds and posts new items to Mastodon. Several feeds and accounts
  run in the same container, with per-feed settings as `FEED_<NAME>_<FIELD>` and global defaults
  (see README). An unknown `FEED_*` variable stops startup.
- Duplicate detection on both GUID and cleaned link (without `utm_*`, `fbclid`, `www.`, fragment
  and trailing `/`), stored in SQLite in `/data/masto-rss.db`.
- Flood protection: a new feed posts nothing (except `BACKFILL`, default `0`), items older than
  `MAX_AGE` (default `48h`) are not posted, if more than half of a feed looks new at once nothing is
  posted, and at most `MAX_POSTS_PER_POLL` (default `5`) are posted per round, oldest first.
- Outbox: a post is saved before it is sent and gets an `Idempotency-Key`, so a retry never becomes
  a duplicate. Temporary errors are retried after 1, 5, 15, 60 and then 360 minutes, and on 429 when
  Mastodon's window opens. A post Mastodon rejects (for example 422) is not retried, and a rejected
  token only pauses its own account.
- The text is built from the template `TEMPLATE` (default `{title}\n\n{link}`) and shortened to fit
  by Mastodon's way of counting. `@name` in text from the feed never becomes a mention.
- Startup check of the instance (`/api/v2/instance`) and of every token (scopes and account). When
  the instance changes version or `api_versions`, or Mastodon responds with a `Deprecation` header,
  it shows up in the log and in a notification.
- Signal notifications via signal-cli-rest-api (`SIGNAL_API_URL`, `SIGNAL_NUMBER`): when a feed has
  failed `FAIL_NOTIFY_AFTER` times in a row (default `3`) and when it works again, when Mastodon
  rejects a post or a token, and on anomalies.
- Status page at `http://<container>:8080/status` (`STATUS_PORT`, `0` turns it off) for a combined
  Homepage widget with followers and latest post per account.
- Conditional fetching with ETag and Last-Modified, a 30 s timeout, a 5 MB size limit and a hint in
  the log when a feed address has moved permanently. After errors the wait grows up to 6 h.
- `node src/main.ts import <name> <file>` imports `processed_entries.txt` from the old masto-rss,
  and `node src/main.ts post <name,…|alla> <text>` posts free text to the feeds' accounts.
- Dry run (`DRY_RUN=1`) and a single round (`ONCE=1`).
- Health check in the image. With `read_only: true`, `/tmp` needs to be a tmpfs.
- The image is based on Node 26 (`node:26.11.1-alpine3.24`).
