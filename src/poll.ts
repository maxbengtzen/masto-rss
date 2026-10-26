/**
 * Ett varv för ett flöde: hämta, tolka och avgör vad som är nytt och vad som ska postas.
 *
 * Skydden mot flod och dubbletter:
 * - En post räknas som sedd om någon av dess nycklar (GUID eller normaliserad länk) har setts.
 * - Nytt flöde: allt som finns markeras som sett, och bara de `backfill` senaste postas.
 * - Ser mer än hälften av ett flöde med minst tio poster nytt ut på en gång, är något fel
 *   (till exempel nya länkar och GUID för allt): allt markeras som sett och inget postas.
 * - Poster äldre än `maxAgeMs` postas inte.
 * - Högst `maxPostsPerPoll` per varv, äldst först. Resten väntar till nästa varv.
 */
import { createHash } from "node:crypto";
import type { FeedConfig } from "./config.ts";
import { fetchFeed, parse, type FeedItem, type FetchResult, type ParsedFeed, type Redirect } from "./feed.ts";
import { renderStatus, type Limits } from "./format.ts";
import type { Store } from "./store.ts";

/** Nycklar som inte funnits i flödet så här länge glöms bort. */
export const FORGET_AFTER_MS = 30 * 24 * 3600 * 1000;
/** Skickade och misslyckade inlägg sparas så här länge i utkorgen. */
export const KEEP_POSTS_MS = 90 * 24 * 3600 * 1000;
export const ANOMALY_MIN_ITEMS = 10;

/**
 * Inläggets id, som också är dess Idempotency-Key: samma flöde och post ger alltid samma id, så att
 * ett omförsök aldrig blir ett nytt inlägg och samma post inte kan hamna i utkorgen två gånger.
 */
export function postId(feed: string, key: string): string {
  return createHash("sha256").update(`masto-rss\n${feed}\n${key}`).digest("hex").slice(0, 32);
}

export interface Decision {
  /** Att posta, äldst först. */
  post: FeedItem[];
  /** Nya poster som markeras som sedda utan att postas. */
  skipped: FeedItem[];
  /** Nya poster som väntar till nästa varv. */
  deferred: FeedItem[];
  /** Varför allt nytt hoppades över, om det gjordes. */
  reason?: "first-run" | "anomaly";
}

/** Postningsordning: daterade poster äldst först, sedan odaterade i omvänd flödesordning. */
function oldestFirst(items: FeedItem[]): FeedItem[] {
  const dated = items.filter((i) => i.published !== undefined).sort((a, b) => a.published! - b.published!);
  const undated = items.filter((i) => i.published === undefined).reverse();
  return [...dated, ...undated];
}

export function decide(
  items: FeedItem[],
  seen: boolean[],
  firstRun: boolean,
  opts: Pick<FeedConfig, "maxAgeMs" | "maxPostsPerPoll" | "backfill">,
  now: number,
): Decision {
  const fresh = items.filter((_, i) => !seen[i]);
  if (firstRun) {
    const ordered = oldestFirst(fresh);
    const keep = Math.min(opts.backfill, ordered.length);
    return { post: ordered.slice(ordered.length - keep), skipped: ordered.slice(0, ordered.length - keep), deferred: [], reason: "first-run" };
  }
  if (items.length >= ANOMALY_MIN_ITEMS && fresh.length > items.length / 2) {
    return { post: [], skipped: fresh, deferred: [], reason: "anomaly" };
  }
  const cutoff = opts.maxAgeMs ? now - opts.maxAgeMs : -Infinity;
  const tooOld = (i: FeedItem) => i.published !== undefined && i.published < cutoff;
  const ordered = oldestFirst(fresh.filter((i) => !tooOld(i)));
  return {
    post: ordered.slice(0, opts.maxPostsPerPoll),
    skipped: fresh.filter(tooOld),
    deferred: ordered.slice(opts.maxPostsPerPoll),
  };
}

/** Samma post två gånger i ett flöde räknas en gång. */
function unique(items: FeedItem[]): FeedItem[] {
  const keys = new Set<string>();
  return items.filter((item) => {
    if (item.keys.some((k) => keys.has(k))) return false;
    for (const k of item.keys) keys.add(k);
    return true;
  });
}

export interface Queued {
  title: string;
  link?: string;
  /** Inläggets text, som den kommer att postas. */
  status: string;
}

export type PollResult =
  | { status: "unchanged"; redirect?: Redirect; previousFailures: number }
  | {
      status: "ok";
      redirect?: Redirect;
      /** Misslyckade varv i rad före det här. */
      previousFailures: number;
      /** Det som lades i utkorgen (i torrläge: det som skulle ha lagts där). */
      queued: Queued[];
      title?: string;
      language?: string;
      /** Poster i flödet som går att känna igen. */
      items: number;
      /** Poster utan både id och länk, som inte går att känna igen. */
      unrecognized: number;
      decision: Decision;
    };

export class PollError extends Error {
  /** Misslyckade varv i rad, inklusive det här. */
  failures: number;

  constructor(cause: unknown, failures: number) {
    super(cause instanceof Error ? cause.message : String(cause), { cause });
    this.name = cause instanceof Error ? cause.name : "PollError";
    this.failures = failures;
  }
}

export interface PollOptions {
  /** Spara ingenting. */
  dryRun: boolean;
  /** Instansens gränser för inläggens längd. */
  limits: Limits;
  now?: number;
}

export async function pollFeed(feed: FeedConfig, store: Store, { dryRun, limits, now = Date.now() }: PollOptions): Promise<PollResult> {
  const state = store.getFeed(feed.name);
  // ETag och Last-Modified gäller bara adressen de kom från.
  const validators = state?.url === feed.url ? { etag: state.etag, lastModified: state.lastModified } : {};
  const base = { name: feed.name, url: feed.url, primedAt: state?.primedAt };

  let fetched: FetchResult;
  let parsed: ParsedFeed | undefined;
  try {
    fetched = await fetchFeed(feed.url, validators);
    if (fetched.status === "ok") parsed = parse(fetched.text, fetched.url);
  } catch (err) {
    const failures = (state?.failures ?? 0) + 1;
    store.transaction(() => store.saveFeed({ ...base, ...validators, okAt: state?.okAt, checkedAt: now, failures }), dryRun);
    throw new PollError(err, failures);
  }

  if (!parsed || fetched.status !== "ok") {
    store.transaction(() => store.saveFeed({ ...base, ...validators, checkedAt: now, okAt: now, failures: 0 }), dryRun);
    return { status: "unchanged", redirect: fetched.redirect, previousFailures: state?.failures ?? 0 };
  }

  const { etag, lastModified, redirect } = fetched;
  const result = parsed;
  return store.transaction(() => {
    const items = unique(result.items);
    const seen = items.map((item) => store.isSeen(feed.name, item.keys));
    const decision = decide(items, seen, !state?.primedAt, feed, now);

    // Det som ska postas läggs i utkorgen i samma transaktion som det markeras som sett.
    const language = feed.language ?? result.language;
    const queued: Queued[] = [];
    for (const item of decision.post) {
      const status = renderStatus(
        feed.template,
        { title: item.title, link: item.link, summary: item.summary, author: item.author, feed: result.title },
        limits,
      );
      const id = postId(feed.name, item.keys[0]);
      if (store.enqueue({ id, feed: feed.name, title: item.title, link: item.link, status, language }, now)) {
        queued.push({ title: item.title, link: item.link, status });
      }
    }

    const deferred = new Set(decision.deferred);
    for (const item of items) if (!deferred.has(item)) store.markSeen(feed.name, item.keys, now);
    store.prune(feed.name, now - FORGET_AFTER_MS);
    store.prunePosts(now - KEEP_POSTS_MS);

    store.saveFeed({
      ...base,
      // Väntar poster får nästa varv inte svara 304, då skulle de bli kvar tills flödet ändras.
      etag: deferred.size ? undefined : etag,
      lastModified: deferred.size ? undefined : lastModified,
      primedAt: state?.primedAt ?? now,
      checkedAt: now,
      okAt: now,
      failures: 0,
    });
    return {
      status: "ok",
      redirect,
      previousFailures: state?.failures ?? 0,
      queued,
      title: result.title,
      language: result.language,
      items: items.length,
      unrecognized: result.skipped,
      decision,
    };
  }, dryRun);
}
