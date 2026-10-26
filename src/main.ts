/**
 * Startpunkt: läser konfigurationen, öppnar databasen, kontrollerar instanser och tokens, och kör
 * varje flöde på sin egen timer: hämta, lägg det nya i utkorgen, posta.
 *
 *   node src/main.ts                                kör tills SIGTERM (ONCE=1: ett varv och avsluta)
 *   node src/main.ts import <namn> <fil>            importerar den gamla botens processed_entries.txt
 *   node src/main.ts post [--language sv] <namn,…|alla> <text>
 *                                                   postar en fri text till flödenas konton
 *
 * Flödena körs ett i taget, så att minnestoppen blir låg. Efter ett misslyckat varv växer
 * väntetiden (intervallet gånger 2, 4, 8 … upp till 6 h, eller längre om servern bett om det).
 */
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import type { Server } from "node:http";
import { join } from "node:path";
import { ConfigError, loadConfig, type Config, type FeedConfig } from "./config.ts";
import { deliver } from "./deliver.ts";
import { HttpError, normalizeLink } from "./feed.ts";
import type { Limits } from "./format.ts";
import { debug, describe, log, setDebug } from "./log.ts";
import { canPost, instanceInfo, MastodonError, postStatus, setDeprecationHandler, verifyAccount, verifyApp, type AccountInfo } from "./mastodon.ts";
import { createNotifier } from "./notify.ts";
import { FORGET_AFTER_MS, PollError, pollFeed, type PollResult } from "./poll.ts";
import { ago, daysAgo, startStatusServer, type FeedStatus, type StatusSnapshot } from "./status.ts";
import { Store } from "./store.ts";
import { VERSION } from "./version.ts";

const HEARTBEAT_FILE = "/tmp/masto-rss.alive";
const HEARTBEAT_MS = 60_000;
const MAX_BACKOFF_MS = 6 * 3600_000;
const ACCOUNT_REFRESH_MS = 3600_000;
const INSTANCE_REFRESH_MS = 24 * 3600_000;
const DEFAULT_LIMITS: Limits = { maxCharacters: 500, charactersPerUrl: 23 };
const TZ = process.env.TZ || undefined;

function exit(message: string): never {
  console.error(message);
  process.exit(1);
}

let config: Config;
try {
  config = loadConfig();
} catch (err) {
  if (err instanceof ConfigError) exit(err.message);
  throw err;
}
setDebug(config.debug);

const dbPath = join(config.dataDir, "masto-rss.db");
let store: Store;
try {
  mkdirSync(config.dataDir, { recursive: true });
  store = new Store(dbPath);
} catch (err) {
  exit(`Kan inte öppna databasen ${dbPath}: ${describe(err)}`);
}

const notify = createNotifier(config.signalUrl, config.signalNumber, config.dryRun);

/** Ett konto är en instans och en token. Flera flöden kan dela konto. */
interface Account {
  instance: string;
  token: string;
  feeds: FeedConfig[];
  info?: AccountInfo;
  /** Varför kontot inte kan posta, om det inte kan. */
  blocked?: string;
}

const accounts = new Map<string, Account>();
const accountKey = (feed: FeedConfig) => `${feed.mastodonUrl}\n${feed.token}`;
for (const feed of config.feeds) {
  const account = accounts.get(accountKey(feed)) ?? { instance: feed.mastodonUrl, token: feed.token, feeds: [] };
  account.feeds.push(feed);
  accounts.set(accountKey(feed), account);
}
const accountOf = (feed: FeedConfig) => accounts.get(accountKey(feed))!;
const host = (url: string) => new URL(url).host;
const names = (account: Account) => account.feeds.map((f) => f.name).join(", ");
const oneLine = (text: string) => text.replaceAll("\n", " ⏎ ");

const [command, ...args] = process.argv.slice(2);

if (command === "import") {
  const [name, file] = args;
  const feed = config.feeds.find((f) => f.name === name?.toLowerCase());
  if (!feed || !file) {
    exit(`Användning: node src/main.ts import <namn> <fil>, där namn är ett av: ${config.feeds.map((f) => f.name).join(", ")}`);
  }
  const lines = readFileSync(file, "utf8").split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  const keys = [...new Set(lines.map((l) => `l:${normalizeLink(l)}`))];
  const added = store.transaction(() => store.importKeys(feed.name, feed.url, keys, Date.now()), config.dryRun);
  log(`${feed.name}: ${config.dryRun ? "skulle importera" : "importerade"} ${lines.length} länkar från ${file} (${added} nya nycklar)`);
  store.close();
  process.exit(0);
}

if (command === "post") {
  let language: string | undefined;
  if (args[0] === "--language") language = args.splice(0, 2)[1];
  const [which = "", ...words] = args;
  const text = words.join(" ").replaceAll("\\n", "\n").trim();
  const wanted = which === "alla" ? config.feeds.map((f) => f.name) : which.toLowerCase().split(",").filter(Boolean);
  if (!text || !wanted.length || wanted.some((n) => !config.feeds.some((f) => f.name === n))) {
    exit(`Användning: node src/main.ts post [--language sv] <namn,…|alla> <text>, där namn är: ${config.feeds.map((f) => f.name).join(", ")}`);
  }
  let ok = true;
  for (const account of accounts.values()) {
    const feed = account.feeds.find((f) => wanted.includes(f.name));
    if (!feed) continue;
    if (config.dryRun) {
      log(`${feed.name}: skulle posta: ${oneLine(text)}`);
      continue;
    }
    // Samma text till samma konto ger samma nyckel, så en upprepning inom en timme blir inget nytt inlägg.
    const key = createHash("sha256").update(`masto-rss post\n${accountKey(feed)}\n${text}`).digest("hex").slice(0, 32);
    try {
      const status = await postStatus(account.instance, account.token, { status: text, visibility: feed.visibility, language: language ?? feed.language }, key);
      log(`${feed.name}: postat ${status.url}`);
    } catch (err) {
      ok = false;
      log(`${feed.name}: FEL: ${describe(err)}`);
    }
  }
  store.close();
  process.exit(ok ? 0 : 1);
}

if (command) exit(`Okänt kommando "${command}". Användning: node src/main.ts [import <namn> <fil> | post <namn,…|alla> <text>]`);

function duration(ms: number): string {
  if (ms % 3_600_000 === 0) return `${ms / 3_600_000} h`;
  if (ms % 60_000 === 0) return `${ms / 60_000} min`;
  return `${Math.round(ms / 1000)} s`;
}

log(`masto-rss ${VERSION} startar${config.dryRun ? " i torrläge: inget postas och inget sparas" : ""}`);
for (const feed of config.feeds) {
  log(`  ${feed.name}: ${feed.url} → ${host(feed.mastodonUrl)}, var ${duration(feed.intervalMs)}, ${feed.visibility}, språk ${feed.language ?? "från flödet"}`);
}
if (config.paused.length) log(`  pausade: ${config.paused.join(", ")}`);

const configured = [...config.feeds.map((f) => f.name), ...config.paused];
for (const { name, lastUsed } of store.orphans(configured)) {
  if (!config.dryRun && Date.now() - lastUsed > FORGET_AFTER_MS) {
    store.transaction(() => store.forget(name));
    log(`${name}: fanns inte i konfigurationen på 30 dagar, historiken är borttagen`);
  } else {
    log(`${name}: finns i databasen men inte i konfigurationen; historiken tas bort efter 30 dagar`);
  }
}

setDeprecationHandler((endpoint, value) => {
  const message = `Mastodon säger att ${endpoint} är på väg bort (Deprecation: ${value}). masto-rss behöver uppdateras.`;
  if (config.dryRun) log(message);
  else if (store.firstNotice(`deprecation ${endpoint}`, Date.now())) {
    log(message);
    void notify(message);
  }
});

/** Instansernas gränser och API-version, per adress. */
const instances = new Map<string, Limits & { apiVersion: number }>();

async function checkInstance(url: string): Promise<void> {
  try {
    const info = await instanceInfo(url);
    // Utan api_versions (äldre Mastodon eller en annan server) räknas länkar i full längd, för säkerhets skull.
    instances.set(url, {
      maxCharacters: info.maxCharacters,
      charactersPerUrl: info.apiVersion ? info.charactersPerUrl : undefined,
      apiVersion: info.apiVersion,
    });
    const before = store.getInstance(url);
    const minor = (v: string) => v.split(".").slice(0, 2).join(".");
    if (!before) {
      log(`${host(url)}: Mastodon ${info.version}, API-version ${info.apiVersion}, ${info.maxCharacters} tecken per inlägg`);
    } else if (before.apiVersion !== info.apiVersion || minor(before.version) !== minor(info.version)) {
      const message =
        `${host(url)} har gått från Mastodon ${before.version} (API ${before.apiVersion}) till ${info.version} ` +
        `(API ${info.apiVersion}). Läs Mastodons CHANGELOG och se om masto-rss behöver ändras.`;
      log(message);
      void notify(message);
    } else if (before.version !== info.version) {
      log(`${host(url)}: Mastodon ${before.version} → ${info.version}`);
    }
    if (!config.dryRun) store.saveInstance(url, info.version, info.apiVersion, Date.now());
  } catch (err) {
    log(`${host(url)}: kunde inte läsa instansens uppgifter (${describe(err)}); räknar med ${DEFAULT_LIMITS.maxCharacters} tecken`);
  }
}

function block(account: Account, reason: string): void {
  if (account.blocked) return;
  account.blocked = reason;
  const message = `${names(account)}: ${reason}. Inget postas för kontot förrän det är rättat och masto-rss startats om.`;
  log(message);
  void notify(message);
}

async function checkAccount(account: Account, first: boolean): Promise<void> {
  if (first) {
    try {
      const app = await verifyApp(account.instance, account.token);
      if (!canPost(app.scopes)) block(account, `token saknar write:statuses (har ${app.scopes.join(", ") || "inga"})`);
    } catch (err) {
      if (err instanceof MastodonError && err.kind === "auth") block(account, `token nekas (${err.message})`);
      else log(`${names(account)}: kunde inte kontrollera token (${describe(err)})`);
    }
  }
  if (account.blocked) return;
  try {
    account.info = await verifyAccount(account.instance, account.token);
    if (first) {
      const { acct, followers, bot } = account.info;
      log(`${names(account)}: @${acct}@${host(account.instance)}, ${followers} följare${bot ? "" : ". Kontot är inte märkt som bot (Inställningar → Profil)"}`);
    }
  } catch (err) {
    // Kontot behövs bara för statussidan och kontrollen efter en timme, inte för att posta.
    if (first) log(`${names(account)}: kunde inte läsa kontot (${describe(err)})`);
  }
}

/** Adresser som redan fått ett tips om omdirigering, så att det bara loggas en gång. */
const redirectsLogged = new Set<string>();

function report(feed: FeedConfig, result: PollResult): void {
  const name = feed.name;
  if (result.redirect && !redirectsLogged.has(name)) {
    redirectsLogged.add(name);
    if (result.redirect.permanent) log(`${name}: adressen har flyttat permanent till ${result.redirect.to}; uppdatera ${feed.prefix}URL`);
    else debug(`${name}: omdirigeras tillfälligt till ${result.redirect.to}`);
  }
  const quiet = config.once ? log : debug;
  if (result.status === "unchanged") {
    quiet(`${name}: oförändrat (304)`);
    return;
  }

  const { decision, items, unrecognized, queued } = result;
  if (unrecognized) debug(`${name}: ${unrecognized} poster saknar både id och länk och hoppas över`);
  if (decision.reason === "first-run") {
    const rest = decision.post.length ? `, ${decision.post.length} läggs i utkorgen (BACKFILL)` : ", inget postas";
    log(`${name}: nytt flöde, ${decision.skipped.length} poster markerade som sedda${rest}`);
  } else if (decision.reason === "anomaly") {
    log(
      `${name}: VARNING: ${decision.skipped.length} av ${items} poster ser nya ut på en gång. Allt markeras som ` +
        "sett och inget postas. Har sajten bytt adresser eller GUID?",
    );
  } else if (decision.skipped.length) {
    log(`${name}: hoppar över ${decision.skipped.length} poster äldre än ${duration(feed.maxAgeMs)}`);
  }
  for (const q of queued) {
    if (config.dryRun) log(`${name}: skulle posta: ${oneLine(q.status)}`);
    else debug(`${name}: i utkorgen: ${q.title}`);
  }
  if (decision.deferred.length) {
    log(`${name}: ${decision.deferred.length} till väntar till nästa varv (högst ${feed.maxPostsPerPoll} per varv)`);
  }
  if (!decision.post.length && !decision.skipped.length && !decision.deferred.length) {
    quiet(`${name}: inget nytt (${items} poster)`);
  }
}

/** Skickar det som väntar i flödets utkorg. */
async function send(feed: FeedConfig): Promise<void> {
  const account = accountOf(feed);
  if (config.dryRun || account.blocked) return;
  const result = await deliver(feed, store, { id: account.info?.id, quotes: (instances.get(feed.mastodonUrl)?.apiVersion ?? 0) >= 7 });
  for (const { post, url, alreadyThere } of result.posted) {
    log(`${feed.name}: ${alreadyThere ? "fanns redan" : "postat"} ${url} (${post.title || "utan titel"})`);
  }
  for (const post of result.expired) log(`${feed.name}: postas inte, väntade längre än ${duration(feed.maxAgeMs)}: ${post.title}`);
  for (const { post, error } of result.failed) {
    log(`${feed.name}: Mastodon nekade "${post.title}": ${error}`);
    void notify(`Mastodon nekade ett inlägg från ${feed.name} (${error}): ${post.title}`);
  }
  if (result.retry) {
    const at = new Date(result.retry.nextAttemptAt).toLocaleTimeString("sv-SE", { timeZone: TZ, hour: "2-digit", minute: "2-digit" });
    log(`${feed.name}: kunde inte posta (${result.retry.error}); nytt försök tidigast ${at}`);
  }
  if (result.authError) block(account, `token nekas (${result.authError.message})`);
}

/** Kör ett varv och returnerar tiden till nästa. */
async function runFeed(feed: FeedConfig): Promise<{ ok: boolean; nextMs: number }> {
  let outcome: { ok: boolean; nextMs: number };
  try {
    const result = await pollFeed(feed, store, { dryRun: config.dryRun, limits: instances.get(feed.mastodonUrl) ?? DEFAULT_LIMITS });
    report(feed, result);
    if (result.previousFailures >= config.failNotifyAfter) void notify(`${feed.name}: flödet fungerar igen`);
    if (result.status === "ok" && result.decision.reason === "anomaly") {
      void notify(
        `${feed.name}: ${result.decision.skipped.length} av ${result.items} poster såg nya ut på en gång. ` +
          "Allt markerades som sett och inget postades. Har sajten bytt adresser eller GUID?",
      );
    }
    outcome = { ok: true, nextMs: Math.round(feed.intervalMs * (0.9 + Math.random() * 0.2)) };
  } catch (err) {
    const failures = err instanceof PollError ? err.failures : 1;
    const cause = err instanceof Error && err.cause ? err.cause : err;
    const retryAfterMs = cause instanceof HttpError && cause.retryAfter !== undefined ? cause.retryAfter * 1000 : 0;
    const nextMs = Math.min(Math.max(feed.intervalMs * 2 ** (failures - 1), retryAfterMs), MAX_BACKOFF_MS);
    log(`${feed.name}: FEL (${failures} i rad): ${describe(cause)}; nästa försök om ${Math.max(1, Math.round(nextMs / 60_000))} min`);
    if (failures === config.failNotifyAfter) {
      void notify(`${feed.name}: flödet har inte gått att hämta ${failures} gånger i rad (${describe(cause)})`);
    }
    outcome = { ok: false, nextMs };
  }
  // Utkorgen töms även när hämtningen misslyckas: där kan finnas inlägg från tidigare varv.
  try {
    await send(feed);
  } catch (err) {
    log(`${feed.name}: fel vid postning: ${describe(err)}`);
  }
  return outcome;
}

for (const url of new Set(config.feeds.map((f) => f.mastodonUrl))) await checkInstance(url);
for (const account of accounts.values()) await checkAccount(account, true);

if (config.once) {
  let ok = true;
  for (const feed of config.feeds) ok = (await runFeed(feed)).ok && ok;
  store.close();
  process.exit(ok ? 0 : 1);
}

function snapshot(): StatusSnapshot {
  const now = Date.now();
  const feeds: FeedStatus[] = config.feeds.map((feed) => {
    const account = accountOf(feed);
    const failures = store.getFeed(feed.name)?.failures ?? 0;
    const problem = account.blocked ?? (failures ? `flödet svarar inte (${failures} i rad)` : undefined);
    const last = store.lastPosted(feed.name);
    const lastText = last
      ? ago(last, now)
      : account.info?.lastStatusAt
        ? daysAgo(account.info.lastStatusAt, now, TZ)
        : "inga inlägg än";
    const followers = account.info ? `${new Intl.NumberFormat("sv-SE").format(account.info.followers)} följare · ` : "";
    return {
      name: feed.name,
      account: account.info ? `@${account.info.acct}` : feed.name,
      url: account.info?.url,
      followers: account.info?.followers,
      posts: account.info?.posts,
      lastPost: last ? new Date(last).toISOString() : undefined,
      ok: !problem,
      summary: problem ? `fel: ${problem}` : `${followers}${lastText}`,
    };
  });
  return {
    version: VERSION,
    totals: { feeds: feeds.length, postedLastDay: store.postedSince(now - 86_400_000), errors: feeds.filter((f) => !f.ok).length },
    feeds,
  };
}

let statusServer: Server | undefined;
if (config.statusPort) {
  statusServer = startStatusServer(config.statusPort, snapshot);
  statusServer.on("error", (err) => log(`Statussidan kunde inte starta på port ${config.statusPort}: ${describe(err)}`));
}

function heartbeat(): void {
  try {
    writeFileSync(HEARTBEAT_FILE, "");
  } catch (err) {
    log(`Kan inte skriva ${HEARTBEAT_FILE}, hälsokontrollen kommer att fallera: ${describe(err)}`);
    clearInterval(heartbeatTimer);
  }
}
const heartbeatTimer = setInterval(heartbeat, HEARTBEAT_MS);
heartbeat();

const refreshTimers = [
  setInterval(() => {
    for (const account of accounts.values()) void checkAccount(account, false);
  }, ACCOUNT_REFRESH_MS),
  setInterval(() => {
    for (const url of instances.keys()) void checkInstance(url);
  }, INSTANCE_REFRESH_MS),
];

const timers = new Map<string, NodeJS.Timeout>();
let queue = Promise.resolve();
let stopping = false;

function schedule(feed: FeedConfig, delayMs: number): void {
  timers.set(
    feed.name,
    setTimeout(() => {
      queue = queue.then(async () => {
        if (stopping) return;
        const { nextMs } = await runFeed(feed);
        if (!stopping) schedule(feed, nextMs);
      });
    }, delayMs),
  );
}

// Sprid de första hämtningarna några sekunder, så att de inte startar samtidigt.
config.feeds.forEach((feed, i) => schedule(feed, i * 3000 + Math.random() * 2000));
if (!config.feeds.length) log("Alla flöden är pausade; väntar utan att göra något");

async function shutdown(signal: string): Promise<void> {
  if (stopping) return;
  stopping = true;
  log(`${signal}: avslutar`);
  for (const timer of [...timers.values(), ...refreshTimers]) clearTimeout(timer);
  clearInterval(heartbeatTimer);
  statusServer?.close();
  // Ett pågående varv får en stund på sig att bli klart.
  await Promise.race([queue, new Promise((resolve) => setTimeout(resolve, 10_000))]);
  store.close();
  process.exit(0);
}
process.on("SIGTERM", () => void shutdown("SIGTERM"));
process.on("SIGINT", () => void shutdown("SIGINT"));
