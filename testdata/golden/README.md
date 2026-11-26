# Golden files

The outputs the TypeScript version (v1.0.3, Node 26 with ICU for Unicode 17) produced, recorded
with `scripts/golden.ts` before the rewrite in Go (the script was removed along with the TypeScript
code and is in the history). The tests require the Go version to produce the same:

| File | What | Test |
|---|---|---|
| `feeds.json` | Parsed feeds (feedsmith and the normalization in `feed.ts`) for everything in `../fixtures/` | `internal/feed` |
| `format.json` | Post text, length and defused mentions for many templates, limits and texts | `internal/format` |
| `config.json` | `parseDuration` and the configuration or errors for many environments, in the same order | `internal/config` |
| `text.json` | `normalizeLink`, `new URL(link, base)`, entities, HTML to text, character encodings, `Date.parse`, time ago, post ids | several |
| `schema.json` | The database schema and `user_version` | `internal/store` |
| `runs.json` | The whole program against a fake server: the log, `/status`, the posts and the notifications | `internal/app` |

All feeds in `../fixtures/` are invented, and partly written in Swedish on purpose so that non-ASCII
text is exercised. `rss-wordpress-example.xml` and `rss-cms-example.xml` have the same structure as
WordPress and a common news CMS (CDATA, `content:encoded`, `dc:creator`, tracking parameters, long
texts), and the rest are written for edge cases. `../unicode/GraphemeBreakTest.txt` is Unicode's own
test cases for grapheme clusters (17.0.0), which `internal/format` also runs.

Real feeds are not version controlled, since the texts belong to others. You can save your own in
`../local/fixtures/` and record `../local/golden/feeds.json` and `format.json` from the TypeScript
version (see `scripts/golden.ts` in the history, with `FIXTURES` and `GOLDEN` set). The tests
`TestParseGoldenLocal` and `TestFormatGoldenLocal` then compare against them, and are skipped
otherwise.

The files are the contract with whoever runs the service: don't change them because a test fails.
If an output changes on purpose, the file is updated by hand in the same commit, and the difference
is described below.

## Intentional differences

- **All messages are in English.** The TypeScript version wrote configuration errors, usage texts,
  log lines, notifications and status summaries in Swedish. `config.json` and `runs.json` were
  updated by hand with the English texts; nothing else in them changed. Some invented input values
  in `config.json` were translated along with them (for example `kanske` → `maybe`), and fixtures
  with Swedish file names were renamed, which changes the keys in `feeds.json`. `post` takes `all`
  as well as the old `alla`. Counts use the singular for one ("1 item", "1 time"), which the Swedish
  text didn't need.
- **The usage texts** say `masto-rss import …` and `masto-rss post …` instead of
  `node src/main.ts …`. The test replaces the text in the golden file before comparing.
- **A lone surrogate** from a numeric entity (`&#xD800;`) becomes U+FFFD. JavaScript kept it in the
  string, but it became U+FFFD anyway when the text was written as UTF-8, so no post differs.
- **The key for `post`** contains the instance's URL and therefore the fake server's port; the test
  computes it instead of reading it from `runs.json`.

Differences the golden files don't cover, since they concern errors and rare input:

- The reason in parentheses in the log for network errors (`TypeError: fetch failed (…)`), for
  broken XML (`MalformedError: Invalid feed format (…)`) and when the status page's port is taken is
  Go's own text, not Node's.
- XML is parsed with `encoding/xml` instead of fast-xml-parser. The fields that are read (title,
  link, description and so on) are read as raw text the same way, but XML that is broken outside
  them can fail in one and not the other.
- Character encodings: UTF-8, UTF-16, windows-1252 (which is also what ISO-8859-1, latin1 and ascii
  mean) and ISO-8859-15. Others are decoded as UTF-8, as an unknown encoding was before.
- Domain names with non-ASCII characters are converted to Punycode without Unicode normalization.
- The number of items "missing both id and link" (only in the debug log) can differ for items that
  only contain unknown elements.
- Notifications on their way when the program exits (`ONCE=1`) are sent before it exits.
- An interval above 24.8 days works; in Node it became a 1 ms timer.
