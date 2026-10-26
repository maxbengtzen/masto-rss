import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import { loadConfig, type FeedConfig } from "../src/config.ts";
import type { FeedItem } from "../src/feed.ts";
import { decide, FORGET_AFTER_MS, PollError, pollFeed, postId } from "../src/poll.ts";
import { Store } from "../src/store.ts";
import { mockFetch, numbered, rss, type TestItem } from "./helpers.ts";

const http = mockFetch();
const NOW = Date.UTC(2026, 9, 26, 12, 30);
const LIMITS = { maxCharacters: 500, charactersPerUrl: 23 };
const HOUR = 3600_000;

const feed: FeedConfig = loadConfig({
  MASTODON_URL: "https://mastodon.example",
  FEED_NEWS_URL: "https://example.com/feed/",
  FEED_NEWS_TOKEN: "t",
}).feeds[0];

let store: Store;
beforeEach(() => {
  store = new Store(":memory:");
});

function item(n: number, hoursAgo?: number): FeedItem {
  return {
    keys: [`g:${n}`],
    title: `Post ${n}`,
    summary: "",
    published: hoursAgo === undefined ? undefined : NOW - hoursAgo * HOUR,
  };
}

const titles = (items: FeedItem[]) => items.map((i) => i.title);

/** Låter flödet svara med de här posterna (och en ETag) vid nästa hämtning. */
function serve(items: TestItem[], etag?: string): void {
  http.set(() => new Response(rss(items), { headers: etag ? { etag } : {} }));
}

test("decide: nytt flöde markerar allt som sett, utom de BACKFILL senaste", () => {
  const items = [item(3, 1), item(2, 2), item(1, 3)];
  const opts = { maxAgeMs: 48 * HOUR, maxPostsPerPoll: 5, backfill: 0 };
  let d = decide(items, [false, false, false], true, opts, NOW);
  assert.equal(d.reason, "first-run");
  assert.deepEqual(titles(d.post), []);
  assert.equal(d.skipped.length, 3);

  d = decide(items, [false, false, false], true, { ...opts, backfill: 2 }, NOW);
  assert.deepEqual(titles(d.post), ["Post 2", "Post 3"]);
  assert.deepEqual(titles(d.skipped), ["Post 1"]);
});

test("decide: nya poster postas äldst först, med tak och väntande rest", () => {
  const items = [item(8, 1), item(7, 2), item(6, 3), item(5, 4), item(4, 5), item(3, 6), item(2, 7), item(1, 8), item(0, 9), item(-1, 10)];
  const seen = items.map((_, i) => i >= 3); // 8, 7 och 6 är nya
  const d = decide(items, seen, false, { maxAgeMs: 48 * HOUR, maxPostsPerPoll: 2, backfill: 0 }, NOW);
  assert.equal(d.reason, undefined);
  assert.deepEqual(titles(d.post), ["Post 6", "Post 7"]);
  assert.deepEqual(titles(d.deferred), ["Post 8"]);
  assert.deepEqual(d.skipped, []);
});

test("decide: för gamla poster hoppas över, odaterade postas sist", () => {
  const items = [item(4), item(3, 1), item(2, 50), item(1, 2)];
  const d = decide(items, [false, false, false, false], false, { maxAgeMs: 48 * HOUR, maxPostsPerPoll: 5, backfill: 0 }, NOW);
  assert.deepEqual(titles(d.skipped), ["Post 2"]);
  assert.deepEqual(titles(d.post), ["Post 1", "Post 3", "Post 4"]);

  const off = decide(items, [false, false, false, false], false, { maxAgeMs: 0, maxPostsPerPoll: 5, backfill: 0 }, NOW);
  assert.equal(off.skipped.length, 0);
});

test("decide: mer än hälften nytt i ett stort flöde är en avvikelse", () => {
  const items = Array.from({ length: 10 }, (_, i) => item(i, i));
  const seen = items.map((_, i) => i < 4); // sex av tio nya
  const d = decide(items, seen, false, { maxAgeMs: 0, maxPostsPerPoll: 5, backfill: 0 }, NOW);
  assert.equal(d.reason, "anomaly");
  assert.equal(d.skipped.length, 6);
  assert.deepEqual(d.post, []);

  // Ett litet flöde med mest nytt är normalt.
  const small = items.slice(0, 4);
  assert.equal(decide(small, [false, false, false, true], false, { maxAgeMs: 0, maxPostsPerPoll: 5, backfill: 0 }, NOW).reason, undefined);
});

test("pollFeed: första varvet läser in flödet, nästa postar bara det nya", async () => {
  serve(numbered(1, 5), '"v1"');
  let result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.equal(result.status, "ok");
  assert.ok(result.status === "ok" && result.decision.reason === "first-run" && result.items === 5);
  assert.equal(result.status === "ok" && result.language, "sv");

  serve(numbered(1, 7), '"v2"');
  result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW + HOUR });
  assert.equal(http.requests[0].headers.get("if-none-match"), '"v1"');
  assert.ok(result.status === "ok");
  assert.deepEqual(titles(result.decision.post), ["Post 6", "Post 7"]);

  serve(numbered(1, 7));
  result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW + 2 * HOUR });
  assert.ok(result.status === "ok" && result.decision.post.length === 0);
  assert.equal(store.getFeed("news")?.etag, undefined);
});

test("pollFeed: 304 behåller tillståndet", async () => {
  serve(numbered(1, 3), '"v1"');
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  http.set(() => new Response(null, { status: 304 }));
  const result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW + HOUR });
  assert.equal(result.status, "unchanged");
  const state = store.getFeed("news");
  assert.equal(state?.etag, '"v1"');
  assert.equal(state?.checkedAt, NOW + HOUR);
  assert.equal(state?.primedAt, NOW);
});

test("pollFeed: torrläget sparar ingenting", async () => {
  serve(numbered(1, 3), '"v1"');
  const result = await pollFeed(feed, store, { dryRun: true, limits: LIMITS, now: NOW });
  assert.ok(result.status === "ok" && result.decision.reason === "first-run");
  assert.equal(store.getFeed("news"), undefined);
  assert.equal(store.isSeen("news", ["g:https://example.com/?p=1"]), false);
});

test("pollFeed: importerade länkar räknas som sedda, även med andra utm-parametrar", async () => {
  store.transaction(() =>
    store.importKeys("news", feed.url, ["l:example.com/post-1", "l:example.com/post-2"], NOW - 24 * HOUR),
  );
  serve([
    { guid: "https://example.com/?p=3", link: "https://example.com/post-3/?utm_source=x", title: "Post 3", date: new Date(NOW - HOUR).toUTCString() },
    { guid: "https://example.com/?p=2", link: "https://www.example.com/post-2/?utm_campaign=y", title: "Post 2", date: new Date(NOW - 2 * HOUR).toUTCString() },
    { guid: "https://example.com/?p=1", link: "https://example.com/post-1", title: "Post 1", date: new Date(NOW - 3 * HOUR).toUTCString() },
  ]);
  const result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.ok(result.status === "ok" && result.decision.reason === undefined);
  assert.deepEqual(titles(result.decision.post), ["Post 3"]);
  // GUID-nyckeln sparas nu också, så att en ändrad länk senare inte ger en dubblett.
  assert.equal(store.isSeen("news", ["g:https://example.com/?p=2"]), true);
});

test("pollFeed: väntande poster tas nästa varv, och då utan ETag", async () => {
  const limited = { ...feed, maxPostsPerPoll: 1 };
  serve(numbered(1, 10), '"v1"');
  await pollFeed(limited, store, { dryRun: false, limits: LIMITS, now: NOW });

  serve(numbered(1, 13), '"v2"');
  let result = await pollFeed(limited, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.ok(result.status === "ok");
  assert.deepEqual(titles(result.decision.post), ["Post 11"]);
  assert.deepEqual(titles(result.decision.deferred), ["Post 12", "Post 13"]);
  assert.equal(store.getFeed("news")?.etag, undefined);

  serve(numbered(1, 13), '"v2"');
  result = await pollFeed(limited, store, { dryRun: false, limits: LIMITS, now: NOW + HOUR });
  assert.equal(http.requests[0].headers.get("if-none-match"), null);
  assert.ok(result.status === "ok");
  assert.deepEqual(titles(result.decision.post), ["Post 12"]);
});

test("pollFeed: fel räknas i rad och nollställs när det fungerar", async () => {
  serve(numbered(1, 2));
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });

  http.set(() => new Response("fel", { status: 502, statusText: "Bad Gateway" }));
  for (const expected of [1, 2]) {
    await assert.rejects(pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW }), (err: unknown) => {
      assert.ok(err instanceof PollError);
      assert.equal(err.failures, expected);
      assert.equal(err.message, "HTTP 502 Bad Gateway");
      return true;
    });
  }
  assert.equal(store.getFeed("news")?.failures, 2);

  serve(numbered(1, 2));
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.equal(store.getFeed("news")?.failures, 0);
});

test("pollFeed: ett fel på ett nytt flöde gör det inte till ett inläst flöde", async () => {
  http.set(() => new Response("<html>nej</html>"));
  await assert.rejects(pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW }), PollError);
  assert.equal(store.getFeed("news")?.primedAt, undefined);

  serve(numbered(1, 3));
  const result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.ok(result.status === "ok" && result.decision.reason === "first-run");
});

test("pollFeed: ny adress skickar inte den gamla adressens ETag", async () => {
  serve(numbered(1, 2), '"v1"');
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  serve(numbered(1, 2));
  await pollFeed({ ...feed, url: "https://example.com/ny-feed/" }, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.equal(http.requests[0].headers.get("if-none-match"), null);
});

test("pollFeed: nycklar som lämnat flödet glöms efter 30 dagar", async () => {
  serve(numbered(1, 3));
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  serve(numbered(3, 3));
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW + FORGET_AFTER_MS - HOUR });
  assert.equal(store.isSeen("news", ["g:https://example.com/?p=1"]), true);
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW + FORGET_AFTER_MS + HOUR });
  assert.equal(store.isSeen("news", ["g:https://example.com/?p=1"]), false);
  assert.equal(store.isSeen("news", ["g:https://example.com/?p=3"]), true);
});

test("pollFeed: samma post två gånger i flödet räknas en gång", async () => {
  serve(numbered(1, 1));
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  const twice = numbered(2, 2);
  serve([...twice, ...twice, ...numbered(1, 1)]);
  const result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.ok(result.status === "ok");
  assert.deepEqual(titles(result.decision.post), ["Post 2"]);
});

test("pollFeed: nya poster läggs i utkorgen med färdig text, men inte i torrläge", async () => {
  serve(numbered(1, 2));
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });

  serve(numbered(1, 3));
  const dry = await pollFeed(feed, store, { dryRun: true, limits: LIMITS, now: NOW });
  assert.ok(dry.status === "ok");
  assert.deepEqual(dry.queued.map((q) => q.status), ["Post 3\n\nhttps://example.com/post-3/?utm_source=rss"]);
  assert.deepEqual(store.duePosts("news", NOW), []);

  serve(numbered(1, 3));
  const live = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.ok(live.status === "ok" && live.queued.length === 1);
  const [post] = store.duePosts("news", NOW);
  assert.equal(post.id, postId("news", "g:https://example.com/?p=3"));
  assert.equal(post.language, "sv");
  assert.equal(post.status, "Post 3\n\nhttps://example.com/post-3/?utm_source=rss");
});

test("pollFeed: misslyckade varv i rad rapporteras när det fungerar igen", async () => {
  serve(numbered(1, 2));
  await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  http.set(() => new Response("fel", { status: 500 }));
  await assert.rejects(pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW }));
  serve(numbered(1, 2));
  const result = await pollFeed(feed, store, { dryRun: false, limits: LIMITS, now: NOW });
  assert.equal(result.previousFailures, 1);
});
