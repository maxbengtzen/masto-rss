import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import { Store } from "../src/store.ts";

const dir = mkdtempSync(join(tmpdir(), "masto-rss-store-"));
after(() => rmSync(dir, { recursive: true, force: true }));

test("databasen skapas, versioneras och behåller sitt innehåll", () => {
  const path = join(dir, "a.db");
  let store = new Store(path);
  store.transaction(() => store.markSeen("news", ["g:1"], 1000));
  store.close();

  store = new Store(path);
  assert.equal(store.isSeen("news", ["g:1"]), true);
  assert.equal((store.db.prepare("pragma user_version").get() as { user_version: number }).user_version, 3);
  assert.equal((store.db.prepare("pragma journal_mode").get() as { journal_mode: string }).journal_mode, "wal");
  store.close();
});

test("en databas från en nyare version öppnas inte", () => {
  const path = join(dir, "b.db");
  const db = new DatabaseSync(path);
  db.exec("pragma user_version = 99");
  db.close();
  assert.throws(() => new Store(path), /schemaversion 99/);
});

test("transaktioner rullas tillbaka i torrläge och vid fel", () => {
  const store = new Store(":memory:");
  store.transaction(() => store.markSeen("news", ["g:1"], 1000), true);
  assert.equal(store.isSeen("news", ["g:1"]), false);
  assert.throws(() =>
    store.transaction(() => {
      store.markSeen("news", ["g:2"], 1000);
      throw new Error("avbrutet");
    }),
  );
  assert.equal(store.isSeen("news", ["g:2"]), false);
});

test("markSeen flyttar fram senast sedd, prune tar bara bort det gamla", () => {
  const store = new Store(":memory:");
  store.markSeen("news", ["g:1", "l:a"], 1000);
  store.markSeen("news", ["g:2"], 1000);
  store.markSeen("news", ["g:1"], 5000);
  store.markSeen("other", ["g:9"], 1000);
  assert.equal(store.prune("news", 2000), 2);
  assert.equal(store.isSeen("news", ["g:1"]), true);
  assert.equal(store.isSeen("news", ["l:a", "g:2"]), false);
  assert.equal(store.isSeen("other", ["g:9"]), true);
});

test("importKeys räknar nya nycklar och markerar flödet som inläst", () => {
  const store = new Store(":memory:");
  assert.equal(store.importKeys("news", "https://example.com/feed", ["l:a", "l:b"], 1000), 2);
  assert.equal(store.importKeys("news", "https://example.com/feed", ["l:b", "l:c"], 2000), 1);
  const state = store.getFeed("news");
  assert.equal(state?.primedAt, 1000);
  assert.equal(state?.url, "https://example.com/feed");
});

test("orphans och forget hittar och tar bort flöden som inte längre finns", () => {
  const store = new Store(":memory:");
  store.saveFeed({ name: "news", url: "https://example.com/a", failures: 0, checkedAt: 5000 });
  store.saveFeed({ name: "old", url: "https://example.com/b", failures: 0, primedAt: 1000 });
  store.markSeen("old", ["g:1"], 1000);
  assert.deepEqual(store.orphans(["news"]), [{ name: "old", lastUsed: 1000 }]);
  store.forget("old");
  assert.deepEqual(store.orphans(["news"]), []);
  assert.equal(store.isSeen("old", ["g:1"]), false);
});

test("en databas från fas 1 (schemaversion 1) uppgraderas och behåller sitt innehåll", () => {
  const path = join(dir, "v1.db");
  const db = new DatabaseSync(path);
  db.exec(`create table feed (name text primary key, url text not null, etag text, last_modified text, primed_at integer,
             checked_at integer, ok_at integer, failures integer not null default 0) strict;
           create table seen (feed text not null, key text not null, first_seen integer not null, last_seen integer not null,
             primary key (feed, key)) strict, without rowid;
           insert into seen values ('news', 'g:1', 1, 1);
           insert into feed (name, url, primed_at, failures) values ('news', 'https://example.com/feed/', 1, 0);
           pragma user_version = 1;`);
  db.close();
  const store = new Store(path);
  assert.equal((store.db.prepare("pragma user_version").get() as { user_version: number }).user_version, 3);
  assert.equal(store.isSeen("news", ["g:1"]), true);
  assert.equal(store.getFeed("news")?.primedAt, 1);
  assert.equal(store.getFeed("news")?.newestPublished, undefined);
  store.saveFeed({ name: "news", url: "https://example.com/feed/", primedAt: 1, failures: 0, newestPublished: 5000 });
  assert.equal(store.getFeed("news")?.newestPublished, 5000);
  store.close();
});

test("utkorgen: samma id läggs bara in en gång, och inlägg tas äldst först", () => {
  const store = new Store(":memory:");
  const post = (id: string) => ({ id, feed: "news", title: id, link: `https://example.com/${id}`, status: id, language: "sv" });
  assert.equal(store.enqueue(post("b"), 2000), true);
  assert.equal(store.enqueue(post("a"), 1000), true);
  assert.equal(store.enqueue(post("a"), 3000), false);
  assert.deepEqual(store.duePosts("news", 5000).map((p) => p.id), ["a", "b"]);
  assert.deepEqual(store.duePosts("news", 1500).map((p) => p.id), ["a"]);

  store.markRetry("a", 9000, "timeout");
  store.markPosted("b", "https://mastodon.example/@news/1", 6000);
  assert.deepEqual(store.duePosts("news", 5000), []);
  assert.equal(store.getPost("a")?.attempts, 1);
  assert.equal(store.lastPosted("news"), 6000);
  assert.equal(store.postedSince(5000), 1);
  assert.equal(store.prunePosts(2500), 1);
  assert.equal(store.getPost("b"), undefined);
  assert.equal(store.getPost("a")?.state, "pending");
});

test("instanser och engångsaviseringar sparas", () => {
  const store = new Store(":memory:");
  assert.equal(store.getInstance("https://mastodon.example"), undefined);
  store.saveInstance("https://mastodon.example", "4.7.3", 11, 1000);
  store.saveInstance("https://mastodon.example", "4.8.0", 12, 2000);
  assert.deepEqual(store.getInstance("https://mastodon.example"), { version: "4.8.0", apiVersion: 12 });
  assert.equal(store.firstNotice("deprecation GET /x", 1000), true);
  assert.equal(store.firstNotice("deprecation GET /x", 2000), false);
});
