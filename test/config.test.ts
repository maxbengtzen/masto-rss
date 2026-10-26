import assert from "node:assert/strict";
import { test } from "node:test";
import { ConfigError, loadConfig, parseDuration } from "../src/config.ts";

const base = {
  MASTODON_URL: "https://mastodon.example/",
  FEED_NEWS_URL: "https://example.com/feed/",
  FEED_NEWS_TOKEN: "token-1",
};

function problems(env: Record<string, string>): string[] {
  try {
    loadConfig(env);
  } catch (err) {
    assert.ok(err instanceof ConfigError);
    return err.problems;
  }
  assert.fail("väntade ConfigError");
}

test("parseDuration förstår sekunder och enheter", () => {
  assert.equal(parseDuration("900"), 900_000);
  assert.equal(parseDuration("15m"), 900_000);
  assert.equal(parseDuration("1h30m"), 5_400_000);
  assert.equal(parseDuration("2d"), 172_800_000);
  assert.equal(parseDuration(" 45S "), 45_000);
  assert.equal(parseDuration("0"), 0);
  assert.equal(parseDuration(""), null);
  assert.equal(parseDuration("15 min"), null);
  assert.equal(parseDuration("m15"), null);
});

test("standardvärden för ett minimalt flöde", () => {
  const config = loadConfig(base);
  assert.equal(config.feeds.length, 1);
  assert.deepEqual(config.feeds[0], {
    name: "news",
    prefix: "FEED_NEWS_",
    url: "https://example.com/feed/",
    token: "token-1",
    mastodonUrl: "https://mastodon.example",
    intervalMs: 900_000,
    visibility: "public",
    language: undefined,
    template: "{title}\n\n{link}",
    quotePolicy: undefined,
    maxAgeMs: 48 * 3600_000,
    maxPostsPerPoll: 5,
    backfill: 0,
  });
  assert.deepEqual(config.paused, []);
  assert.equal(config.dataDir, "/data");
  assert.equal(config.dryRun, false);
  assert.equal(config.once, false);
});

test("globala värden gäller alla flöden, egna värden går före", () => {
  const config = loadConfig({
    ...base,
    INTERVAL: "30m",
    VISIBILITY: "unlisted",
    FEED_OTHER_URL: "https://example.org/rss",
    FEED_OTHER_TOKEN: "token-2",
    FEED_OTHER_INTERVAL: "1h",
    FEED_OTHER_MASTODON_URL: "https://other.example",
    FEED_OTHER_LANGUAGE: "sv-SE",
    FEED_OTHER_QUOTE_POLICY: "Nobody",
    DRY_RUN: "1",
    ONCE: "true",
    DATA_DIR: "/tmp/x",
  });
  const [news, other] = config.feeds;
  assert.equal(news.name, "news");
  assert.equal(news.intervalMs, 1_800_000);
  assert.equal(news.visibility, "unlisted");
  assert.equal(other.name, "other");
  assert.equal(other.intervalMs, 3_600_000);
  assert.equal(other.visibility, "unlisted");
  assert.equal(other.mastodonUrl, "https://other.example");
  assert.equal(other.language, "sv");
  assert.equal(other.quotePolicy, "nobody");
  assert.equal(config.dryRun, true);
  assert.equal(config.once, true);
  assert.equal(config.dataDir, "/tmp/x");
});

test("namn med understreck och fält med understreck går isär", () => {
  const config = loadConfig({
    MASTODON_URL: "https://mastodon.example",
    FEED_FASTIGHETS_SVERIGE_URL: "https://example.com/feed",
    FEED_FASTIGHETS_SVERIGE_TOKEN: "t",
    FEED_FASTIGHETS_SVERIGE_MAX_POSTS_PER_POLL: "2",
  });
  assert.equal(config.feeds[0].name, "fastighets_sverige");
  assert.equal(config.feeds[0].maxPostsPerPoll, 2);
});

test("en global LANGUAGE från systemet (gettext) används inte", () => {
  const config = loadConfig({ ...base, LANGUAGE: "en_US:en" });
  assert.equal(config.feeds[0].language, undefined);
});

test("\\n i mallen blir radbrytning", () => {
  const config = loadConfig({ ...base, TEMPLATE: "{title}\\n{link} #nyheter" });
  assert.equal(config.feeds[0].template, "{title}\n{link} #nyheter");
});

test("MAX_AGE=0 stänger av åldersspärren", () => {
  assert.equal(loadConfig({ ...base, MAX_AGE: "0" }).feeds[0].maxAgeMs, 0);
});

test("ENABLED=false pausar flödet utan att kräva resten", () => {
  const config = loadConfig({ ...base, FEED_OLD_ENABLED: "false", FEED_OLD_URL: "inte en adress" });
  assert.deepEqual(config.paused, ["old"]);
  assert.deepEqual(config.feeds.map((f) => f.name), ["news"]);
});

test("alla fel rapporteras på en gång", () => {
  const found = problems({
    FEED_NEWS_URL: "ftp://example.com/feed",
    FEED_NEWS_TEMPLTE: "{title}",
    FEED_OTHER_URL: "https://example.org/",
    FEED_OTHER_INTERVAL: "30",
    FEED_OTHER_VISIBILITY: "everyone",
    FEED_OTHER_TEMPLATE: "{title} {body}",
    FEED_OTHER_MAX_POSTS_PER_POLL: "0",
  });
  const expected = [
    "Okänd inställning FEED_NEWS_TEMPLTE (stavfel?)",
    'FEED_NEWS_URL är ingen http(s)-adress: "ftp://example.com/feed"',
    "FEED_NEWS_TOKEN saknas",
    "MASTODON_URL saknas (globalt eller som FEED_NEWS_MASTODON_URL)",
    "FEED_OTHER_TOKEN saknas",
    "FEED_OTHER_INTERVAL är för kort: minst 60 s",
    'FEED_OTHER_MAX_POSTS_PER_POLL måste vara ett heltal, minst 1: "0"',
    'FEED_OTHER_VISIBILITY måste vara public, unlisted, private, direct: "everyone"',
  ];
  for (const p of expected) assert.ok(found.includes(p), `saknar: ${p}\nfick: ${found.join("\n")}`);
  assert.ok(found.some((p) => p.startsWith("FEED_OTHER_TEMPLATE har okänt fält {body}")));
});

test("ett ogiltigt ENABLED stoppar starten", () => {
  assert.deepEqual(problems({ ...base, FEED_NEWS_ENABLED: "kanske" }), [
    'FEED_NEWS_ENABLED måste vara true eller false, inte "kanske"',
  ]);
});

test("MASTODON_URL måste vara en instans, inte en sida", () => {
  assert.deepEqual(problems({ ...base, MASTODON_URL: "https://mastodon.example/@konto" }), [
    "MASTODON_URL ska vara instansens adress, till exempel https://mastodon.nu",
  ]);
});

test("samma flöde till samma konto två gånger stoppas", () => {
  assert.deepEqual(problems({ ...base, FEED_COPY_URL: base.FEED_NEWS_URL, FEED_COPY_TOKEN: base.FEED_NEWS_TOKEN }), [
    "FEED_COPY_ och FEED_NEWS_ postar samma flöde till samma konto",
  ]);
});

test("utan flöden blir det ett tydligt fel", () => {
  assert.deepEqual(problems({ MASTODON_URL: "https://mastodon.example" }), [
    "Inga flöden: ange minst FEED_<NAMN>_URL och FEED_<NAMN>_TOKEN",
  ]);
});
