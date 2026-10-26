/**
 * Tömmer utkorgen för ett flöde: skickar väntande inlägg äldst först, ett i taget.
 *
 * - Lyckat: inlägget markeras som postat, med sin adress.
 * - Mastodon nekar för gott (till exempel 422): misslyckat, och nästa inlägg tas.
 * - Token nekas (401/403): inget mer skickas för kontot, och inlägget väntar kvar.
 * - Tillfälligt fel: inlägget väntar och provas igen, efter 1, 5, 15, 60 och sedan 360 minuter.
 *   Vid 429 väntar det tills Mastodons fönster öppnar igen.
 * - Har ett inlägg väntat längre än Mastodon minns Idempotency-Key (en timme), kontrolleras kontots
 *   senaste inlägg först, så att ett inlägg som faktiskt kom fram inte postas två gånger.
 * - Har det väntat längre än MAX_AGE (till exempel medan en token var trasig) skickas det inte alls.
 */
import type { FeedConfig } from "./config.ts";
import { decodeEntities, normalizeLink } from "./feed.ts";
import { MastodonError, postStatus, recentStatuses } from "./mastodon.ts";
import type { Post, Store } from "./store.ts";

export const IDEMPOTENCY_WINDOW_MS = 3600_000;
const RETRY_MINUTES = [1, 5, 15, 60, 360];
/** Paus mellan två inlägg till samma konto, så att de kommer i ordning och inte i en klump. */
const SPACING_MS = 2000;

export interface Account {
  /** Kontots id. Behövs för kontrollen av ett inlägg som väntat längre än en timme. */
  id?: string;
  /** Sant om instansen kan ta emot citatinställningen (API-version 7 eller senare). */
  quotes: boolean;
}

export interface DeliveryResult {
  posted: Array<{ post: Post; url: string; alreadyThere: boolean }>;
  failed: Array<{ post: Post; error: string }>;
  /** Inlägg som väntat längre än MAX_AGE och därför aldrig skickades. */
  expired: Post[];
  /** Ett tillfälligt fel stoppade utskicket; resten väntar till nästa försök. */
  retry?: { post: Post; error: string; nextAttemptAt: number };
  /** Token nekas. Inget mer skickas för kontot. */
  authError?: MastodonError;
}

export interface DeliveryOptions {
  now?: () => number;
  sleep?: (ms: number) => Promise<unknown>;
}

/** Adressen till ett av kontots senaste inlägg som länkar till samma sak, om något gör det. */
async function findExisting(feed: FeedConfig, accountId: string, link: string): Promise<string | undefined> {
  const wanted = normalizeLink(link);
  try {
    for (const status of await recentStatuses(feed.mastodonUrl, feed.token, accountId)) {
      const hrefs = [...status.content.matchAll(/href="([^"]+)"/g)].map((m) => decodeEntities(m[1]));
      if ([status.card, ...hrefs].some((url) => url && normalizeLink(url) === wanted)) return status.url;
    }
  } catch {
    // Går kontrollen inte att göra postas inlägget som vanligt.
  }
  return undefined;
}

export async function deliver(feed: FeedConfig, store: Store, account: Account, options: DeliveryOptions = {}): Promise<DeliveryResult> {
  const now = options.now ?? Date.now;
  const sleep = options.sleep ?? ((ms: number) => new Promise((resolve) => setTimeout(resolve, ms)));
  const result: DeliveryResult = { posted: [], failed: [], expired: [] };

  for (const [i, post] of store.duePosts(feed.name, now()).entries()) {
    if (feed.maxAgeMs && now() - post.createdAt > feed.maxAgeMs) {
      store.markFailed(post.id, "väntade längre än MAX_AGE");
      result.expired.push(post);
      continue;
    }
    if (i > 0) await sleep(SPACING_MS);
    try {
      if (post.attempts > 0 && account.id && post.link && now() - post.createdAt > IDEMPOTENCY_WINDOW_MS) {
        const existing = await findExisting(feed, account.id, post.link);
        if (existing) {
          store.markPosted(post.id, existing, now());
          result.posted.push({ post, url: existing, alreadyThere: true });
          continue;
        }
      }
      const status = await postStatus(
        feed.mastodonUrl,
        feed.token,
        {
          status: post.status,
          visibility: feed.visibility,
          language: post.language,
          quotePolicy: account.quotes ? feed.quotePolicy : undefined,
        },
        post.id,
      );
      store.markPosted(post.id, status.url, now());
      result.posted.push({ post, url: status.url, alreadyThere: false });
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      if (err instanceof MastodonError && err.kind === "auth") {
        store.markRetry(post.id, now() + 3600_000, message);
        result.authError = err;
        break;
      }
      if (err instanceof MastodonError && err.kind === "rejected") {
        store.markFailed(post.id, message);
        result.failed.push({ post, error: message });
        continue;
      }
      const resetAt = err instanceof MastodonError && err.status === 429 ? err.resetAt : undefined;
      const delayMs = resetAt
        ? Math.max(resetAt - now(), 60_000)
        : RETRY_MINUTES[Math.min(post.attempts, RETRY_MINUTES.length - 1)] * 60_000;
      const nextAttemptAt = now() + delayMs;
      store.markRetry(post.id, nextAttemptAt, message);
      result.retry = { post, error: message, nextAttemptAt };
      break;
    }
  }
  return result;
}
