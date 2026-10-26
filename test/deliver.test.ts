import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";
import { loadConfig } from "../src/config.ts";
import { deliver, IDEMPOTENCY_WINDOW_MS } from "../src/deliver.ts";
import { Store } from "../src/store.ts";
import { mockFetch } from "./helpers.ts";

const http = mockFetch();
const NOW = Date.UTC(2026, 9, 26, 12);
const feed = loadConfig({
  MASTODON_URL: "https://mastodon.example",
  FEED_NEWS_URL: "https://example.com/feed/",
  FEED_NEWS_TOKEN: "t",
  FEED_NEWS_QUOTE_POLICY: "followers",
}).feeds[0];
const options = { now: () => NOW, sleep: async () => {} };

let store: Store;
let bodies: Array<Record<string, unknown>>;
beforeEach(() => {
  store = new Store(":memory:");
  bodies = [];
});

function queue(id: string, title: string, createdAt = NOW) {
  store.enqueue({ id, feed: "news", title, link: `https://example.com/${id}/`, status: `${title}\n\nhttps://example.com/${id}/`, language: "sv" }, createdAt);
}

/** Låtsas vara Mastodon. `reply` avgör svaret på varje POST. */
function mastodon(reply: (n: number) => Response) {
  let n = 0;
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    http.requests.push({ url, headers: new Headers(init?.headers) });
    if (init?.method === "POST") {
      bodies.push(JSON.parse(String(init.body)));
      return reply(++n);
    }
    // Kontots senaste inlägg: ett som länkar till a.
    return Response.json([{ url: "https://mastodon.example/@news/1", content: '<p>A <a href="https://example.com/a/?utm_source=rss&amp;utm_medium=feed">länk</a></p>', card: null }]);
  }) as typeof fetch;
  http.requests.length = 0;
}

test("väntande inlägg skickas äldst först, med nyckel, språk och citatinställning", async () => {
  queue("a", "Första", NOW - 2000);
  queue("b", "Andra", NOW - 1000);
  mastodon((n) => Response.json({ id: String(n), url: `https://mastodon.example/@news/${n}` }));
  const result = await deliver(feed, store, { quotes: true }, options);
  assert.deepEqual(result.posted.map((p) => [p.post.title, p.url]), [["Första", "https://mastodon.example/@news/1"], ["Andra", "https://mastodon.example/@news/2"]]);
  assert.deepEqual(http.requests.map((r) => r.headers.get("idempotency-key")), ["a", "b"]);
  assert.deepEqual(bodies[0], { status: "Första\n\nhttps://example.com/a/", visibility: "public", language: "sv", quote_approval_policy: "followers" });
  assert.equal(store.getPost("a")?.state, "posted");
  assert.deepEqual(store.duePosts("news", NOW), []);
});

test("citatinställningen skickas inte till en instans som inte kan ta emot den", async () => {
  queue("a", "Första");
  mastodon(() => Response.json({ id: "1", url: "u" }));
  await deliver(feed, store, { quotes: false }, options);
  assert.equal("quote_approval_policy" in bodies[0] && bodies[0].quote_approval_policy !== undefined, false);
});

test("nekat inlägg (422) misslyckas för gott, nästa skickas ändå", async () => {
  queue("a", "För långt", NOW - 2000);
  queue("b", "Okej", NOW - 1000);
  mastodon((n) => (n === 1 ? Response.json({ error: "Validation failed" }, { status: 422 }) : Response.json({ id: "2", url: "u2" })));
  const result = await deliver(feed, store, { quotes: true }, options);
  assert.deepEqual(result.failed.map((f) => [f.post.title, f.error]), [["För långt", "HTTP 422: Validation failed"]]);
  assert.deepEqual(result.posted.map((p) => p.post.title), ["Okej"]);
  assert.equal(store.getPost("a")?.state, "failed");
});

test("tillfälligt fel och 429 stoppar utskicket och provas igen senare", async () => {
  queue("a", "Första", NOW - 2000);
  queue("b", "Andra", NOW - 1000);
  mastodon(() => new Response("upptagen", { status: 503 }));
  let result = await deliver(feed, store, { quotes: true }, options);
  assert.equal(result.retry?.nextAttemptAt, NOW + 60_000);
  assert.equal(bodies.length, 1);
  assert.equal(store.getPost("a")?.attempts, 1);
  assert.deepEqual(store.duePosts("news", NOW).map((p) => p.id), ["b"]);

  const reset = "2026-10-26T12:30:00Z";
  mastodon(() => Response.json({ error: "Too many requests" }, { status: 429, headers: { "x-ratelimit-reset": reset } }));
  result = await deliver(feed, store, { quotes: true }, { ...options, now: () => NOW + 60_000 });
  assert.equal(result.retry?.post.id, "a");
  assert.equal(result.retry?.nextAttemptAt, Date.parse(reset));
});

test("token som nekas stoppar kontot, inlägget väntar kvar", async () => {
  queue("a", "Första");
  mastodon(() => Response.json({ error: "The access token was revoked" }, { status: 401 }));
  const result = await deliver(feed, store, { quotes: true }, options);
  assert.equal(result.authError?.status, 401);
  assert.equal(store.getPost("a")?.state, "pending");
});

test("efter en timme kontrolleras kontot innan ett inlägg skickas igen", async () => {
  queue("a", "Kom fram ändå", NOW - IDEMPOTENCY_WINDOW_MS - 1000);
  queue("b", "Kom aldrig fram", NOW - IDEMPOTENCY_WINDOW_MS - 500);
  store.markRetry("a", NOW, "timeout");
  store.markRetry("b", NOW, "timeout");
  mastodon(() => Response.json({ id: "7", url: "https://mastodon.example/@news/7" }));
  const result = await deliver(feed, store, { id: "42", quotes: true }, options);
  assert.deepEqual(result.posted.map((p) => [p.post.id, p.alreadyThere]), [["a", true], ["b", false]]);
  assert.equal(bodies.length, 1);
  assert.ok(http.requests.some((r) => r.url.startsWith("https://mastodon.example/api/v1/accounts/42/statuses")));
});

test("inlägg som väntat längre än MAX_AGE skickas inte", async () => {
  queue("a", "Gammalt", NOW - feed.maxAgeMs - 1000);
  mastodon(() => Response.json({ id: "1", url: "u" }));
  const result = await deliver(feed, store, { quotes: true }, options);
  assert.deepEqual(result.expired.map((p) => p.id), ["a"]);
  assert.equal(bodies.length, 0);
  assert.equal(store.getPost("a")?.state, "failed");
});
