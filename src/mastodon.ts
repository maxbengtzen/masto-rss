/**
 * Mastodon-API:t: instansinfo, kontroll av token, postning och kontots senaste inlägg.
 *
 * Alla anrop har timeout. Fel klassas: nätverk, 5xx och 429 är tillfälliga, 401/403 betyder att
 * token inte duger, och övriga 4xx att Mastodon nekar för gott. Ett Deprecation-huvud i ett svar
 * skickas vidare till `onDeprecation`, så att vi märker när något vi använder håller på att fasas ut.
 */
import { USER_AGENT } from "./version.ts";

const TIMEOUT_MS = 30_000;

export class MastodonError extends Error {
  status: number;
  /** När rate limit-fönstret öppnar igen (ms), vid 429. */
  resetAt?: number;

  constructor(status: number, message: string, resetAt?: number) {
    super(message);
    this.name = "MastodonError";
    this.status = status;
    this.resetAt = resetAt;
  }

  /** "auth": token nekas. "rejected": Mastodon säger nej för gott. "transient": prova igen senare. */
  get kind(): "auth" | "rejected" | "transient" {
    if (this.status === 401 || this.status === 403) return "auth";
    if (this.status >= 400 && this.status < 500 && this.status !== 429 && this.status !== 408) return "rejected";
    return "transient";
  }
}

export interface InstanceInfo {
  version: string;
  /** `api_versions.mastodon`; 0 om instansen inte anger det (äldre Mastodon eller annan server). */
  apiVersion: number;
  maxCharacters: number;
  charactersPerUrl: number;
}

export interface AppInfo {
  name: string;
  scopes: string[];
}

export interface AccountInfo {
  id: string;
  acct: string;
  url: string;
  followers: number;
  posts: number;
  /** Datum (YYYY-MM-DD) för kontots senaste inlägg. */
  lastStatusAt?: string;
  bot: boolean;
}

export interface StatusParams {
  status: string;
  visibility: string;
  language?: string;
  quotePolicy?: string;
}

export interface PostedStatus {
  id: string;
  url: string;
}

let onDeprecation: (endpoint: string, value: string) => void = () => {};

export function setDeprecationHandler(handler: (endpoint: string, value: string) => void): void {
  onDeprecation = handler;
}

interface CallOptions {
  method?: string;
  token?: string;
  body?: unknown;
  idempotencyKey?: string;
}

async function call<T>(base: string, path: string, { method = "GET", token, body, idempotencyKey }: CallOptions = {}): Promise<T> {
  const headers: Record<string, string> = { "User-Agent": USER_AGENT, Accept: "application/json" };
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;

  const res = await fetch(base + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });
  const deprecation = res.headers.get("deprecation");
  if (deprecation) onDeprecation(`${method} ${path.split("?")[0].replace(/\/\d+(?=\/|$)/g, "/:id")}`, deprecation);

  const text = await res.text();
  if (!res.ok) {
    let detail = text.slice(0, 200);
    try {
      detail = (JSON.parse(text) as { error?: string }).error ?? detail;
    } catch {
      // inte JSON
    }
    const reset = Date.parse(res.headers.get("x-ratelimit-reset") ?? "");
    throw new MastodonError(res.status, `HTTP ${res.status}${detail ? `: ${detail}` : ""}`, Number.isNaN(reset) ? undefined : reset);
  }
  return (text ? JSON.parse(text) : {}) as T;
}

export async function instanceInfo(base: string): Promise<InstanceInfo> {
  const j = await call<{
    version?: string;
    api_versions?: { mastodon?: number };
    configuration?: { statuses?: { max_characters?: number; characters_reserved_per_url?: number } };
  }>(base, "/api/v2/instance");
  return {
    version: j.version ?? "okänd",
    apiVersion: j.api_versions?.mastodon ?? 0,
    maxCharacters: j.configuration?.statuses?.max_characters ?? 500,
    charactersPerUrl: j.configuration?.statuses?.characters_reserved_per_url ?? 23,
  };
}

/** Appen bakom token och dess scopes. Kräver ingen scope sedan Mastodon 4.3. */
export async function verifyApp(base: string, token: string): Promise<AppInfo> {
  const j = await call<{ name?: string; scopes?: string[] }>(base, "/api/v1/apps/verify_credentials", { token });
  return { name: j.name ?? "", scopes: j.scopes ?? [] };
}

/** Sant om scopes räcker för att posta: `write` eller `write:statuses`. */
export function canPost(scopes: string[]): boolean {
  return scopes.includes("write") || scopes.includes("write:statuses");
}

export async function verifyAccount(base: string, token: string): Promise<AccountInfo> {
  const j = await call<{
    id: string;
    acct: string;
    url: string;
    followers_count?: number;
    statuses_count?: number;
    last_status_at?: string | null;
    bot?: boolean;
  }>(base, "/api/v1/accounts/verify_credentials", { token });
  return {
    id: j.id,
    acct: j.acct,
    url: j.url,
    followers: j.followers_count ?? 0,
    posts: j.statuses_count ?? 0,
    lastStatusAt: j.last_status_at ?? undefined,
    bot: j.bot ?? false,
  };
}

export async function postStatus(base: string, token: string, params: StatusParams, idempotencyKey: string): Promise<PostedStatus> {
  const j = await call<{ id: string; url?: string; uri?: string }>(base, "/api/v1/statuses", {
    method: "POST",
    token,
    idempotencyKey,
    body: {
      status: params.status,
      visibility: params.visibility,
      language: params.language,
      quote_approval_policy: params.quotePolicy,
    },
  });
  return { id: j.id, url: j.url ?? j.uri ?? "" };
}

/** Kontots senaste egna inlägg, utan svar och boostar. */
export async function recentStatuses(base: string, token: string, accountId: string, limit = 40): Promise<Array<{ url: string; content: string; card?: string }>> {
  const list = await call<Array<{ url?: string; uri?: string; content?: string; card?: { url?: string } | null }>>(
    base,
    `/api/v1/accounts/${encodeURIComponent(accountId)}/statuses?limit=${limit}&exclude_replies=true&exclude_reblogs=true`,
    { token },
  );
  return list.map((s) => ({ url: s.url ?? s.uri ?? "", content: s.content ?? "", card: s.card?.url }));
}
