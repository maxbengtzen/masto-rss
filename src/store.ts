/**
 * Databasen: SQLite via node:sqlite, en fil i DATA_DIR.
 *
 * `feed` har ett tillstånd per flöde (ETag, senaste hämtning, fel i rad). `seen` har varje nyckel
 * (GUID eller normaliserad länk) som setts i ett flöde, med när den senast fanns kvar i flödet, så
 * att gamla nycklar kan rensas utan att något som fortfarande finns i flödet glöms bort.
 *
 * `post` är utkorgen: ett inlägg sparas som "pending" i samma transaktion som posten markeras som
 * sedd, och blir "posted" eller "failed" när Mastodon svarat. Id:t skickas som Idempotency-Key.
 * `instance` minns varje instans version, så att en uppgradering märks. `notice` håller reda på
 * aviseringar som bara ska skickas en gång.
 *
 * Schemat versioneras med PRAGMA user_version. Allt som hör till ett varv skrivs i en
 * transaktion; i torrläge rullas den tillbaka.
 */
import { DatabaseSync, type StatementSync } from "node:sqlite";

const MIGRATIONS = [
  `create table feed (
     name text primary key,
     url text not null,
     etag text,
     last_modified text,
     primed_at integer,
     checked_at integer,
     ok_at integer,
     failures integer not null default 0
   ) strict;
   create table seen (
     feed text not null,
     key text not null,
     first_seen integer not null,
     last_seen integer not null,
     primary key (feed, key)
   ) strict, without rowid;`,
  `create table post (
     id text primary key,
     feed text not null,
     title text not null,
     link text,
     status text not null,
     language text,
     state text not null default 'pending',
     attempts integer not null default 0,
     next_attempt_at integer not null,
     created_at integer not null,
     posted_at integer,
     status_url text,
     error text
   ) strict;
   create index post_due on post (feed, state, next_attempt_at);
   create table instance (
     url text primary key,
     version text not null,
     api_version integer not null,
     checked_at integer not null
   ) strict;
   create table notice (
     key text primary key,
     sent_at integer not null
   ) strict;`,
];

/** Inlägg i utkorgen. */
export interface Post {
  id: string;
  feed: string;
  title: string;
  link?: string;
  status: string;
  language?: string;
  state: "pending" | "posted" | "failed";
  attempts: number;
  nextAttemptAt: number;
  createdAt: number;
  postedAt?: number;
  statusUrl?: string;
  error?: string;
}

interface PostRow {
  id: string;
  feed: string;
  title: string;
  link: string | null;
  status: string;
  language: string | null;
  state: Post["state"];
  attempts: number;
  next_attempt_at: number;
  created_at: number;
  posted_at: number | null;
  status_url: string | null;
  error: string | null;
}

function toPost(r: PostRow): Post {
  return {
    id: r.id,
    feed: r.feed,
    title: r.title,
    link: r.link ?? undefined,
    status: r.status,
    language: r.language ?? undefined,
    state: r.state,
    attempts: r.attempts,
    nextAttemptAt: r.next_attempt_at,
    createdAt: r.created_at,
    postedAt: r.posted_at ?? undefined,
    statusUrl: r.status_url ?? undefined,
    error: r.error ?? undefined,
  };
}

export interface FeedState {
  name: string;
  url: string;
  etag?: string;
  lastModified?: string;
  /** När flödet lästes in första gången eller importerades. Saknas = nytt flöde. */
  primedAt?: number;
  checkedAt?: number;
  okAt?: number;
  /** Misslyckade hämtningar i rad. */
  failures: number;
}

interface FeedRow {
  name: string;
  url: string;
  etag: string | null;
  last_modified: string | null;
  primed_at: number | null;
  checked_at: number | null;
  ok_at: number | null;
  failures: number;
}

export class Store {
  db: DatabaseSync;
  #getFeed: StatementSync;
  #saveFeed: StatementSync;
  #hasKey: StatementSync;
  #touchKey: StatementSync;
  #addKey: StatementSync;
  #prune: StatementSync;

  constructor(path: string) {
    this.db = new DatabaseSync(path);
    this.db.exec("pragma journal_mode = wal; pragma synchronous = normal; pragma busy_timeout = 5000;");
    this.#migrate();
    this.#getFeed = this.db.prepare("select * from feed where name = ?");
    this.#saveFeed = this.db.prepare(
      `insert into feed (name, url, etag, last_modified, primed_at, checked_at, ok_at, failures)
       values (?, ?, ?, ?, ?, ?, ?, ?)
       on conflict (name) do update set url = excluded.url, etag = excluded.etag,
         last_modified = excluded.last_modified, primed_at = excluded.primed_at,
         checked_at = excluded.checked_at, ok_at = excluded.ok_at, failures = excluded.failures`,
    );
    this.#hasKey = this.db.prepare("select 1 from seen where feed = ? and key = ?");
    this.#touchKey = this.db.prepare(
      `insert into seen (feed, key, first_seen, last_seen) values (?, ?, ?, ?)
       on conflict (feed, key) do update set last_seen = excluded.last_seen`,
    );
    this.#addKey = this.db.prepare(
      "insert into seen (feed, key, first_seen, last_seen) values (?, ?, ?, ?) on conflict do nothing",
    );
    this.#prune = this.db.prepare("delete from seen where feed = ? and last_seen < ?");
  }

  #migrate(): void {
    const { user_version: version } = this.db.prepare("pragma user_version").get() as { user_version: number };
    if (version > MIGRATIONS.length) {
      throw new Error(`Databasen har schemaversion ${version}, men den här versionen av masto-rss känner bara till ${MIGRATIONS.length}`);
    }
    for (let v = version; v < MIGRATIONS.length; v++) {
      this.transaction(() => {
        this.db.exec(MIGRATIONS[v]);
        this.db.exec(`pragma user_version = ${v + 1}`);
      });
    }
  }

  /** Kör `fn` i en transaktion. Med `rollback` sparas ingenting (torrläge). */
  transaction<T>(fn: () => T, rollback = false): T {
    this.db.exec("begin immediate");
    try {
      const result = fn();
      this.db.exec(rollback ? "rollback" : "commit");
      return result;
    } catch (err) {
      this.db.exec("rollback");
      throw err;
    }
  }

  getFeed(name: string): FeedState | undefined {
    const row = this.#getFeed.get(name) as FeedRow | undefined;
    if (!row) return undefined;
    return {
      name: row.name,
      url: row.url,
      etag: row.etag ?? undefined,
      lastModified: row.last_modified ?? undefined,
      primedAt: row.primed_at ?? undefined,
      checkedAt: row.checked_at ?? undefined,
      okAt: row.ok_at ?? undefined,
      failures: row.failures,
    };
  }

  saveFeed(s: FeedState): void {
    this.#saveFeed.run(
      s.name, s.url, s.etag ?? null, s.lastModified ?? null, s.primedAt ?? null,
      s.checkedAt ?? null, s.okAt ?? null, s.failures,
    );
  }

  /** Sant om någon av nycklarna har setts i flödet. */
  isSeen(feed: string, keys: string[]): boolean {
    return keys.some((key) => this.#hasKey.get(feed, key) !== undefined);
  }

  /** Sparar nycklarna som sedda nu. Finns de redan flyttas bara "senast sedd" fram. */
  markSeen(feed: string, keys: string[], now: number): void {
    for (const key of keys) this.#touchKey.run(feed, key, now, now);
  }

  /** Tar bort nycklar som inte funnits i flödet sedan `before`. Returnerar antalet. */
  prune(feed: string, before: number): number {
    return Number(this.#prune.run(feed, before).changes);
  }

  /**
   * Importerar nycklar (till exempel länkar från den gamla botens tillståndsfil) och markerar
   * flödet som inläst, så att första hämtningen inte behandlas som ett nytt flöde. Returnerar
   * antalet nycklar som inte fanns sedan tidigare.
   */
  importKeys(feed: string, url: string, keys: string[], now: number): number {
    const state = this.getFeed(feed);
    this.saveFeed({ ...(state ?? { name: feed, failures: 0 }), url: state?.url ?? url, primedAt: state?.primedAt ?? now });
    let added = 0;
    for (const key of keys) added += Number(this.#addKey.run(feed, key, now, now).changes);
    return added;
  }

  /** Flöden i databasen som inte finns i konfigurationen, med när de senast hämtades. */
  orphans(configured: string[]): Array<{ name: string; lastUsed: number }> {
    const rows = this.db
      .prepare(
        `select name, coalesce(checked_at, primed_at, 0) as last_used from feed
         where name not in (select value from json_each(?)) order by name`,
      )
      .all(JSON.stringify(configured)) as Array<{ name: string; last_used: number }>;
    return rows.map((r) => ({ name: r.name, lastUsed: r.last_used }));
  }

  /** Glömmer ett flöde helt: tillståndet och alla nycklar. */
  forget(feed: string): void {
    this.db.prepare("delete from seen where feed = ?").run(feed);
    this.db.prepare("delete from feed where name = ?").run(feed);
  }

  /** Lägger ett inlägg i utkorgen. Ett id som redan finns lämnas orört. Sant om det lades till. */
  enqueue(post: Pick<Post, "id" | "feed" | "title" | "link" | "status" | "language">, now: number): boolean {
    const { changes } = this.db
      .prepare(
        `insert into post (id, feed, title, link, status, language, next_attempt_at, created_at)
         values (?, ?, ?, ?, ?, ?, ?, ?) on conflict do nothing`,
      )
      .run(post.id, post.feed, post.title, post.link ?? null, post.status, post.language ?? null, now, now);
    return Number(changes) > 0;
  }

  /** Väntande inlägg för flödet som får skickas nu, äldst först. */
  duePosts(feed: string, now: number): Post[] {
    const rows = this.db
      .prepare("select * from post where feed = ? and state = 'pending' and next_attempt_at <= ? order by created_at, rowid")
      .all(feed, now) as unknown as PostRow[];
    return rows.map(toPost);
  }

  getPost(id: string): Post | undefined {
    const row = this.db.prepare("select * from post where id = ?").get(id) as PostRow | undefined;
    return row && toPost(row);
  }

  markPosted(id: string, statusUrl: string, now: number): void {
    this.db
      .prepare("update post set state = 'posted', posted_at = ?, status_url = ?, error = null, attempts = attempts + 1 where id = ?")
      .run(now, statusUrl, id);
  }

  markRetry(id: string, nextAttemptAt: number, error: string): void {
    this.db.prepare("update post set attempts = attempts + 1, next_attempt_at = ?, error = ? where id = ?").run(nextAttemptAt, error, id);
  }

  markFailed(id: string, error: string): void {
    this.db.prepare("update post set state = 'failed', attempts = attempts + 1, error = ? where id = ?").run(error, id);
  }

  /** Senaste lyckade inlägget per flöde (ms). */
  lastPosted(feed: string): number | undefined {
    const row = this.db.prepare("select max(posted_at) as at from post where feed = ? and state = 'posted'").get(feed) as { at: number | null };
    return row.at ?? undefined;
  }

  /** Antal lyckade inlägg sedan `since` (ms), för alla flöden. */
  postedSince(since: number): number {
    return Number((this.db.prepare("select count(*) as n from post where state = 'posted' and posted_at >= ?").get(since) as { n: number }).n);
  }

  /** Tar bort skickade och misslyckade inlägg som är äldre än `before`. */
  prunePosts(before: number): number {
    return Number(this.db.prepare("delete from post where state != 'pending' and created_at < ?").run(before).changes);
  }

  getInstance(url: string): { version: string; apiVersion: number } | undefined {
    const row = this.db.prepare("select version, api_version from instance where url = ?").get(url) as
      | { version: string; api_version: number }
      | undefined;
    return row && { version: row.version, apiVersion: row.api_version };
  }

  saveInstance(url: string, version: string, apiVersion: number, now: number): void {
    this.db
      .prepare(
        `insert into instance (url, version, api_version, checked_at) values (?, ?, ?, ?)
         on conflict (url) do update set version = excluded.version, api_version = excluded.api_version, checked_at = excluded.checked_at`,
      )
      .run(url, version, apiVersion, now);
  }

  /** Sant första gången för en nyckel; sparar att aviseringen skickats. */
  firstNotice(key: string, now: number): boolean {
    return Number(this.db.prepare("insert into notice (key, sent_at) values (?, ?) on conflict do nothing").run(key, now).changes) > 0;
  }

  close(): void {
    this.db.close();
  }
}
