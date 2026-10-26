import assert from "node:assert/strict";
import { test } from "node:test";
import { canPost, instanceInfo, MastodonError, postStatus, setDeprecationHandler, verifyAccount, verifyApp } from "../src/mastodon.ts";
import { mockFetch } from "./helpers.ts";

const http = mockFetch();
const BASE = "https://mastodon.example";

const json = (body: unknown, init: ResponseInit = {}) =>
  new Response(JSON.stringify(body), { ...init, headers: { "content-type": "application/json", ...init.headers } });

test("instanceInfo läser gränser och API-version, med standardvärden", async () => {
  http.set(() =>
    json({ version: "4.7.3", api_versions: { mastodon: 11 }, configuration: { statuses: { max_characters: 1000, characters_reserved_per_url: 23 } } }),
  );
  assert.deepEqual(await instanceInfo(BASE), { version: "4.7.3", apiVersion: 11, maxCharacters: 1000, charactersPerUrl: 23 });
  assert.equal(http.requests[0].url, `${BASE}/api/v2/instance`);
  assert.equal(http.requests[0].headers.get("authorization"), null);

  http.set(() => json({ version: "0.22.3 git-abc" }));
  assert.deepEqual(await instanceInfo(BASE), { version: "0.22.3 git-abc", apiVersion: 0, maxCharacters: 500, charactersPerUrl: 23 });
});

test("verifyApp, canPost och verifyAccount", async () => {
  http.set(() => json({ name: "masto-rss", scopes: ["read", "profile", "write"] }));
  const app = await verifyApp(BASE, "hemlig");
  assert.deepEqual(app, { name: "masto-rss", scopes: ["read", "profile", "write"] });
  assert.equal(http.requests[0].headers.get("authorization"), "Bearer hemlig");
  assert.ok(canPost(app.scopes));
  assert.ok(canPost(["write:statuses"]));
  assert.ok(!canPost(["read", "write:media"]));

  http.set(() => json({ id: "1", acct: "feber", url: "https://mastodon.example/@feber", followers_count: 195, statuses_count: 36350, last_status_at: "2026-10-26", bot: true }));
  assert.deepEqual(await verifyAccount(BASE, "hemlig"), {
    id: "1",
    acct: "feber",
    url: "https://mastodon.example/@feber",
    followers: 195,
    posts: 36350,
    lastStatusAt: "2026-10-26",
    bot: true,
  });
});

test("postStatus skickar texten, inställningarna och Idempotency-Key", async () => {
  let body: unknown;
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    http.requests.push({ url: String(input), headers: new Headers(init?.headers) });
    body = JSON.parse(String(init?.body));
    return json({ id: "99", url: "https://mastodon.example/@feber/99" });
  }) as typeof fetch;
  http.requests.length = 0;
  const posted = await postStatus(BASE, "hemlig", { status: "Hej\n\nhttps://example.com", visibility: "public", language: "sv", quotePolicy: "nobody" }, "nyckel-1");
  assert.deepEqual(posted, { id: "99", url: "https://mastodon.example/@feber/99" });
  assert.equal(http.requests[0].url, `${BASE}/api/v1/statuses`);
  assert.equal(http.requests[0].headers.get("idempotency-key"), "nyckel-1");
  assert.match(http.requests[0].headers.get("user-agent") ?? "", /^masto-rss\//);
  assert.deepEqual(body, { status: "Hej\n\nhttps://example.com", visibility: "public", language: "sv", quote_approval_policy: "nobody" });
  mockFetch(); // återställ den vanliga låtsade fetch
});

test("fel klassas som token, nekat eller tillfälligt", async () => {
  const cases: Array<[Response, string, MastodonError["kind"]]> = [
    [json({ error: "The access token is invalid" }, { status: 401 }), "HTTP 401: The access token is invalid", "auth"],
    [json({ error: "Validation failed: Text character limit of 500 exceeded" }, { status: 422 }), "HTTP 422: Validation failed: Text character limit of 500 exceeded", "rejected"],
    [new Response("<html>Bad gateway</html>", { status: 502 }), "HTTP 502: <html>Bad gateway</html>", "transient"],
  ];
  for (const [response, message, kind] of cases) {
    const h = mockFetch();
    h.set(() => response);
    await assert.rejects(postStatus(BASE, "t", { status: "x", visibility: "public" }, "k"), (err: unknown) => {
      assert.ok(err instanceof MastodonError);
      assert.equal(err.message, message);
      assert.equal(err.kind, kind);
      return true;
    });
  }
});

test("429 ger tidpunkten då fönstret öppnar igen", async () => {
  const h = mockFetch();
  h.set(() => json({ error: "Too many requests" }, { status: 429, headers: { "x-ratelimit-reset": "2026-10-26T12:05:00.150Z" } }));
  await assert.rejects(postStatus(BASE, "t", { status: "x", visibility: "public" }, "k"), (err: unknown) => {
    assert.ok(err instanceof MastodonError);
    assert.equal(err.kind, "transient");
    assert.equal(err.resetAt, Date.parse("2026-10-26T12:05:00.150Z"));
    return true;
  });
});

test("ett Deprecation-huvud rapporteras med endpointen", async () => {
  const seen: string[] = [];
  setDeprecationHandler((endpoint, value) => seen.push(`${endpoint} ${value}`));
  const h = mockFetch();
  h.set(() => json({ version: "5.0.0" }, { headers: { deprecation: "@1700000000" } }));
  await instanceInfo(BASE);
  assert.deepEqual(seen, ["GET /api/v2/instance @1700000000"]);
});
