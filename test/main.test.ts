import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, beforeEach, test } from "node:test";
import { numbered, rss } from "./helpers.ts";

const MAIN = new URL("../src/main.ts", import.meta.url).pathname;
const root = mkdtempSync(join(tmpdir(), "masto-rss-main-"));

// En riktig lokal HTTP-server som är både flöde, Mastodon och Signal-tjänst. Inget nätverk.
let feedXml = "";
let posts: Array<{ key: string | undefined; body: Record<string, unknown> }> = [];
let signals: string[] = [];

async function readBody(req: IncomingMessage): Promise<string> {
  let body = "";
  for await (const chunk of req) body += chunk;
  return body;
}

const server = createServer(async (req, res) => {
  const send = (status: number, body: unknown) =>
    res.writeHead(status, { "content-type": "application/json" }).end(JSON.stringify(body));
  const authorized = req.headers.authorization === "Bearer bra";
  switch (`${req.method} ${req.url}`) {
    case "GET /old":
      return res.writeHead(301, { location: "/feed" }).end();
    case "GET /feed":
      return res.writeHead(200, { "content-type": "application/rss+xml; charset=utf-8" }).end(feedXml);
    case "GET /api/v2/instance":
      return send(200, { version: "4.7.3", api_versions: { mastodon: 11 }, configuration: { statuses: { max_characters: 500, characters_reserved_per_url: 23 } } });
    case "GET /api/v1/apps/verify_credentials":
      return authorized ? send(200, { name: "test", scopes: ["read", "write"] }) : send(401, { error: "The access token is invalid" });
    case "GET /api/v1/accounts/verify_credentials":
      return authorized
        ? send(200, { id: "1", acct: "news", url: `${base}/@news`, followers_count: 3, statuses_count: 10, last_status_at: "2026-10-26", bot: true })
        : send(401, { error: "The access token is invalid" });
    case "POST /api/v1/statuses": {
      const body = JSON.parse(await readBody(req));
      if (!authorized) return send(401, { error: "The access token is invalid" });
      posts.push({ key: req.headers["idempotency-key"] as string | undefined, body });
      return send(200, { id: String(posts.length), url: `${base}/@news/${posts.length}` });
    }
    case "POST /v2/send":
      signals.push(JSON.parse(await readBody(req)).message);
      return send(201, {});
    default:
      return send(404, { error: "Not found" });
  }
});

let base = "";
before(async () => {
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});
after(() => {
  server.close();
  rmSync(root, { recursive: true, force: true });
});

// main.test.ts kör hela programmet mot den riktiga klockan, med åldersspärren (MAX_AGE 48 h)
// påslagen. Posterna dateras därför relativt till nu, inte till numbered()s fasta datum.
let dataDir = "";
beforeEach(() => {
  feedXml = rss(numbered(1, 3, Date.now()));
  posts = [];
  signals = [];
  dataDir = mkdtempSync(join(root, "data-"));
});

function run(env: Record<string, string>, args: string[] = []): Promise<{ status: number | null; out: string; err: string }> {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [MAIN, ...args], {
      env: { PATH: process.env.PATH, TZ: "UTC", DATA_DIR: dataDir, STATUS_PORT: "0", ...env },
    });
    let out = "";
    let err = "";
    child.stdout.on("data", (d) => (out += d));
    child.stderr.on("data", (d) => (err += d));
    child.on("close", (status) => resolve({ status, out, err }));
  });
}

const env = (extra: Record<string, string> = {}) => ({
  MASTODON_URL: base,
  FEED_NEWS_URL: `${base}/feed`,
  FEED_NEWS_TOKEN: "bra",
  ONCE: "1",
  ...extra,
});

test("utan konfiguration stoppar den med ett tydligt fel", async () => {
  const { status, err } = await run({});
  assert.equal(status, 1);
  assert.match(err, /Inga flöden/);
});

test("ett torrt varv kontrollerar instans och konto, tipsar om flyttad adress och sparar inget", async () => {
  const first = await run(env({ DRY_RUN: "1", FEED_NEWS_URL: `${base}/old` }));
  assert.equal(first.status, 0, first.err);
  assert.match(first.out, /127\.0\.0\.1:\d+: Mastodon 4\.7\.3, API-version 11, 500 tecken per inlägg/);
  assert.match(first.out, /news: @news@127\.0\.0\.1:\d+, 3 följare/);
  assert.match(first.out, /news: adressen har flyttat permanent till http:\/\/127\.0\.0\.1:\d+\/feed; uppdatera FEED_NEWS_URL/);
  assert.match(first.out, /news: nytt flöde, 3 poster markerade som sedda, inget postas/);
  assert.equal(posts.length, 0);

  const second = await run(env({ DRY_RUN: "1" }));
  assert.match(second.out, /news: nytt flöde/);
});

test("ett riktigt varv postar det nya, en gång", async () => {
  const first = await run(env({ BACKFILL: "1" }));
  assert.equal(first.status, 0, first.err);
  assert.match(first.out, /news: postat http:\/\/127\.0\.0\.1:\d+\/@news\/1 \(Post 3\)/);
  assert.equal(posts.length, 1);
  assert.equal(posts[0].body.status, "Post 3\n\nhttps://example.com/post-3/?utm_source=rss");
  assert.equal(posts[0].body.language, "sv");
  assert.equal(posts[0].body.visibility, "public");
  assert.match(posts[0].key ?? "", /^[0-9a-f]{32}$/);

  feedXml = rss(numbered(1, 4, Date.now()));
  const second = await run(env());
  assert.equal(second.status, 0, second.err);
  assert.equal(posts.length, 2);
  assert.equal(posts[1].body.status, "Post 4\n\nhttps://example.com/post-4/?utm_source=rss");

  const third = await run(env());
  assert.equal(third.status, 0, third.err);
  assert.equal(posts.length, 2);
});

test("en token som nekas stoppar kontot och ger en Signal-avisering", async () => {
  const { status, out } = await run(env({ FEED_NEWS_TOKEN: "fel", BACKFILL: "1", SIGNAL_API_URL: base, SIGNAL_NUMBER: "+46700000000" }));
  assert.equal(status, 0);
  assert.match(out, /news: token nekas \(HTTP 401: The access token is invalid\)/);
  assert.equal(posts.length, 0);
  assert.equal(signals.length, 1);
  assert.match(signals[0], /^masto-rss: news: token nekas/);
});

test("import gör att de gamla botarnas poster räknas som sedda", async () => {
  const file = join(dataDir, "processed_entries.txt");
  writeFileSync(file, "https://example.com/post-1/?utm_source=rss\nhttps://example.com/post-2/?utm_source=rss\n\n");
  const imported = await run(env(), ["import", "NEWS", file]);
  assert.equal(imported.status, 0, imported.err);
  assert.match(imported.out, /news: importerade 2 länkar från .* \(2 nya nycklar\)/);

  const dry = await run(env({ DRY_RUN: "1" }));
  assert.equal(dry.status, 0, dry.err);
  assert.match(dry.out, /news: skulle posta: Post 3 ⏎  ⏎ https:\/\/example\.com\/post-3\/\?utm_source=rss/);
  assert.doesNotMatch(dry.out, /Post [12]\b/);
});

test("post skickar en fri text till kontot", async () => {
  const dry = await run(env({ DRY_RUN: "1" }), ["post", "alla", "Hej", "allihop!"]);
  assert.match(dry.out, /news: skulle posta: Hej allihop!/);
  assert.equal(posts.length, 0);

  const live = await run(env(), ["post", "--language", "sv", "news", "Hej\\n\\nallihop!"]);
  assert.equal(live.status, 0, live.err);
  assert.match(live.out, /news: postat http:\/\/127\.0\.0\.1:\d+\/@news\/1/);
  assert.deepEqual(posts[0].body, { status: "Hej\n\nallihop!", visibility: "public", language: "sv" });

  assert.match((await run(env(), ["post", "saknas", "text"])).err, /Användning: .*post/);
});

test("ett flöde som inte går att tolka ger fel och slutkoden 1", async () => {
  feedXml = "<html>inget flöde</html>";
  const { status, out } = await run(env());
  assert.equal(status, 1);
  assert.match(out, /news: FEL \(1 i rad\): .*; nästa försök om 15 min/);
});
