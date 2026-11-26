# Instructions for coding assistants

This file is written for you, an LLM or coding assistant helping with this repository: coding,
testing, versioning, writing release notes and publishing. Read all of it before changing
anything. Commits, code comments, CHANGELOG and all runtime text (logs, notifications, status
output) are written in English (commit subjects follow Conventional Commits, see below).

If the file `AGENTS.local.md` exists in the checkout, read it too. It describes the owner's own
deployment and is not version controlled.

## What it is and where things are

`masto-rss` reads RSS, Atom, RDF and JSON feeds and posts new items to Mastodon. All feeds and
accounts run in one container. It is a small Go service (since v1.1.0; previously TypeScript).

| What | Where |
|---|---|
| Entry point | `cmd/masto-rss/main.go`: the program and `healthcheck` |
| Main loop | `internal/app`: startup checks, scheduling, `import`, `post`, status page content, shutdown |
| Configuration | `internal/config`: environment variables, `FEED_<NAME>_<FIELD>` per feed |
| Fetching and parsing | `internal/feed`: fetching, character encodings, RSS/Atom/RDF (`encoding/xml`) and JSON Feed, links as keys |
| Database | `internal/store`: SQLite (`modernc.org/sqlite`) in `/data/masto-rss.db` |
| Deciding what to post | `internal/poll`: new items, flood and duplicate safeguards, the outbox |
| Post text | `internal/format`: the template, length by Mastodon's rules (grapheme clusters), no mentions |
| Posting | `internal/deliver` (the outbox, retries) and `internal/mastodon` (the API client) |
| Notifications and status | `internal/notify` (Signal via signal-cli-rest-api), `internal/status` (`/status`, for example for Homepage, and `/api/dashboard` for a dashboard) |
| JavaScript semantics | `internal/js` (`Date.parse`, `trim()` etc.) and `internal/weburl` (`new URL()`, `URLSearchParams`), so dates, links and keys stay the same as in the TypeScript version |
| Other | `internal/logx` (log lines in `TZ`), `internal/netx` (errors in the log), `version.go` (version and User-Agent from `VERSION`) |
| Tests | `*_test.go` next to the code (HTTP is faked: no network), invented feeds in `testdata/fixtures/`, the TypeScript version's outputs in `testdata/golden/`; real feeds only locally in `testdata/local/` (not version controlled) |
| Image | `ghcr.io/maxbengtzen/masto-rss`, built by GitHub Actions on `vX.Y.Z` tags |
| Version | `VERSION` |
| Release scripts | `scripts/release.sh` (cuts a release) and `scripts/changelog.sh` (called by `release.sh` and by GitHub Actions) |
| CI | `.github/workflows/publish.yml` |

## Rules

- **No real feeds in the repository.** Article texts belong to others and reveal which feeds are
  run. Fixtures and golden files are built from invented text; real feeds may only live in the
  git-ignored `testdata/local/`. The invented feed content is partly Swedish on purpose, to test
  non-ASCII text.
- **No deployment details in the repository.** Paths, host names, networks and anything else about
  a particular server belong in `AGENTS.local.md` or in the deployment's own compose file, never in
  the repository or a commit.
- **Never test-post to real accounts unless the owner has said so.** Use `DRY_RUN=1`.
- **Ask the owner before anything visible outside the repository:** pushing a version tag (which
  publishes an image and a GitHub Release) and deploying. Committing and pushing ordinary commits
  to `main` is fine when the owner asked for the change.
- **Never reuse a version number.** Never move or delete a published tag. If a release went wrong:
  fix it and release the next patch version.
- **Secrets:** no tokens or phone numbers in code, tests, commits, logs or CHANGELOG. Tests use
  invented values. Never print a token, not even partially.
- **Keep dependencies minimal.** The only dependency beyond Go's standard library is
  `modernc.org/sqlite`. Don't add more without asking. Builds use `CGO_ENABLED=0`, so nothing may
  require cgo.
- **The golden files are the contract.** `testdata/golden/` holds the TypeScript version's outputs.
  Don't change them because a test fails; if a behavior changes on purpose, update the file by hand
  and describe the difference in `testdata/golden/README.md`.
- **The container runs read-only** (`read_only: true`) as 1000:1000 in a `scratch` image. The code
  only writes to `/data` (the database) and `/tmp` (the health file).
- **Measure memory** when something might affect it (new dependency, new cache, larger changes to
  parsing): `docker stats` and `RssAnon` in `/proc/<pid>/status` for the container's process,
  before and after.

## Developing and testing

Without Go installed, run it in the Go image:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.27-alpine \
  sh -c 'gofmt -l . && go vet ./... && go test ./...'
```

Add or update tests for every behavior change. CI runs the Go version that `go.mod` specifies.
Data races between goroutines are found with `go test -race ./...` (requires cgo, for example in
`golang:1.27` with `CGO_ENABLED=1`).

The grapheme tables in `internal/format/tables.go` are generated from the Unicode Character
Database by `internal/format/gen_tables.go`; see the comment there. Only change the Unicode version
together with `testdata/unicode/GraphemeBreakTest.txt`.

### Dry run against real feeds

Build the image and run one round in dry-run mode (`DRY_RUN=1`, `ONCE=1`). Nothing is posted, and
everything written to the database is rolled back. With a real token the account is checked too
(read-only); pass it in through the environment without printing it.

```sh
docker build -t masto-rss:dev .
mkdir -p data
docker run --rm --read-only --tmpfs /tmp -v "$PWD/data:/data" \
  -e DRY_RUN=1 -e ONCE=1 -e STATUS_PORT=0 \
  -e MASTODON_URL=https://mastodon.example \
  -e FEED_NEWS_URL=https://example.com/feed/ -e FEED_NEWS_TOKEN \
  masto-rss:dev
```

With an empty database every feed should log "new feed", and `would post:` shows the posts' text as
it would look. `data/` is ignored by git. Delete it afterwards.

## Commits

Conventional Commits with an English subject: `feat: …`, `fix: …`, `docs: …`, `chore: …`,
`test: …`, `refactor: …`. A breaking change gets `!` (`feat!: …`) and an explanation in the body.
Release commits are created by `scripts/release.sh` (`chore(release): vX.Y.Z`); don't write them by
hand.

## Versioning (semver)

What counts as what. "The user" is whoever runs the container, i.e. the compose file and the
accounts posted to.

| Type | When | Examples |
|---|---|---|
| **Major** (`2.0.0`) | The compose file must change for things to work as before | An environment variable is renamed or removed; a template field is renamed; the database needs a manual step; an image change that requires compose changes |
| **Minor** (`1.1.0`) | New backwards-compatible feature | New optional environment variable; images with alt text; filters; a new template field |
| **Patch** (`1.0.1`) | Fix or maintenance without visible behavior change | Bug fix; updated dependency or base image; better log text |

The image is tagged `X.Y.Z`, `X.Y` and `X`. Anyone running `:1` gets minors and patches on the next
`docker compose pull`; a major requires changing the tag in the compose file.

## Release notes (CHANGELOG.md)

Release notes are written **as you go** in `CHANGELOG.md` under `## [Unreleased]`, in the same
commit as the change. On release, `scripts/release.sh` moves them under a new version heading, and
GitHub Actions uses exactly that section as the GitHub Release text.

- Write in English, for whoever runs the service: what is different and what they need to do, not
  how the code changed.
- Use the headings `### Added`, `### Changed`, `### Fixed`, `### Removed` and `### Security`, in
  that order, only the ones needed.
- Mention new or changed environment variables by name and default value.
- A major starts with `### Required when upgrading` with exact steps (for example which line in the
  compose file to change).

`scripts/release.sh` stops if `Unreleased` is empty.

## Cutting a release

Prerequisites: the working tree is clean, `main` is pushed, and CI on `main` is green
(`gh run list --limit 3`).

1. **Check `CHANGELOG.md`.** Everything changed since the previous version should be under
   `## [Unreleased]`. Compare with `git log --oneline v$(cat VERSION)..HEAD`.
2. **Choose the version** from the table above and **ask the owner** before continuing, with the
   version number and release notes as the basis.
3. **Create the version:**
   ```sh
   scripts/release.sh patch   # or minor / major / an exact number like 1.2.0
   ```
   The script checks that the working tree is clean and that `Unreleased` isn't empty, and runs
   `go vet` and the tests (in the Go image if Go is missing). It then writes the version to
   `VERSION`, moves `Unreleased` to the new version with today's date, commits
   (`chore(release): vX.Y.Z`) and creates an annotated tag `vX.Y.Z`. If it aborts: fix the cause
   and run it again.
4. **Publish:**
   ```sh
   git push --follow-tags
   ```
5. **Follow the build:**
   ```sh
   gh run watch "$(gh run list --branch "v$(cat VERSION)" --limit 1 --json databaseId --jq '.[0].databaseId')" --exit-status
   ```
   The `publish` job checks that the tag matches `VERSION`, builds and pushes the image with the
   tags `X.Y.Z`, `X.Y` and `X`, and creates a GitHub Release with the section from `CHANGELOG.md`.
6. **Check the release:** `gh release view vX.Y.Z`.
7. **Deploy** as described in `AGENTS.local.md`, if it exists, and ask the owner first.

### If something goes wrong

- **CI fails on the tag:** nothing has been published if it failed before the build, but the tag
  remains. Fix it on `main`, add a line about the fix under `Unreleased` and release the next patch
  version. Don't delete the tag.
- **The new version doesn't work in production:** temporarily pin the compose file to the previous
  version, for example `ghcr.io/maxbengtzen/masto-rss:1.0.3`, and release a fix. Then switch back
  to `:1`. The database is the same in all 1.x, so you can roll back without touching it.
- **`scripts/release.sh` says Unreleased is empty:** write the release notes first (see above) and
  commit.

## Maintenance

- **Dependencies:** Dependabot watches `go.mod` (weekly) and GitHub Actions (monthly). An updated
  dependency is released as a patch version.
- **Go:** the `Dockerfile` builds with `golang:1.27-alpine` and runs in `scratch`. A new Go version
  is changed in `go.mod` and the `Dockerfile` together and released as a patch version. CI reads
  the Go version from `go.mod`.
