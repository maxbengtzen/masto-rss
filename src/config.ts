/**
 * Läser konfigurationen ur miljövariabler.
 *
 * Varje flöde har ett namn, och dess inställningar heter FEED_<NAMN>_<FÄLT>. Ett globalt värde
 * (samma fält utan prefix, till exempel INTERVAL) gäller alla flöden som inte har ett eget.
 * Kontrollen är strikt: en okänd FEED_*-variabel eller ett ogiltigt värde stoppar starten, och
 * alla fel rapporteras på en gång.
 */

export const VISIBILITIES = ["public", "unlisted", "private", "direct"] as const;
export type Visibility = (typeof VISIBILITIES)[number];

export const QUOTE_POLICIES = ["public", "followers", "nobody"] as const;
export type QuotePolicy = (typeof QUOTE_POLICIES)[number];

export const TEMPLATE_FIELDS = ["title", "link", "summary", "author", "feed"];

export interface FeedConfig {
  /** Namnet med gemener, till exempel "feber". Flödets identitet i databasen. */
  name: string;
  /** Prefixet i miljövariablerna, till exempel "FEED_FEBER_". */
  prefix: string;
  url: string;
  token: string;
  /** Instansens adress utan avslutande snedstreck. */
  mastodonUrl: string;
  intervalMs: number;
  visibility: Visibility;
  /** ISO 639-kod. Saknas den används flödets eget språk. */
  language?: string;
  template: string;
  quotePolicy?: QuotePolicy;
  /** 0 = ingen åldersgräns. */
  maxAgeMs: number;
  maxPostsPerPoll: number;
  backfill: number;
}

export interface Config {
  /** Flöden som ska köras, sorterade efter namn. */
  feeds: FeedConfig[];
  /** Namn på flöden med ENABLED=false. Deras historik behålls. */
  paused: string[];
  dataDir: string;
  dryRun: boolean;
  once: boolean;
  debug: boolean;
  /** signal-cli-rest-api för aviseringar; tomt = av. */
  signalUrl: string;
  signalNumber: string;
  /** Avisera när ett flöde misslyckats så här många gånger i rad. */
  failNotifyAfter: number;
  /** Port för statussidan (/status); 0 = av. */
  statusPort: number;
}

export class ConfigError extends Error {
  problems: string[];

  constructor(problems: string[]) {
    super(`Fel i konfigurationen:\n${problems.map((p) => `  - ${p}`).join("\n")}`);
    this.name = "ConfigError";
    this.problems = problems;
  }
}

/** Fält som kan sättas både globalt och per flöde. */
const SHARED = ["MASTODON_URL", "INTERVAL", "VISIBILITY", "TEMPLATE", "QUOTE_POLICY", "MAX_AGE", "MAX_POSTS_PER_POLL", "BACKFILL"];
/**
 * Fält som bara finns per flöde. LANGUAGE hör hit eftersom en global LANGUAGE redan är en
 * vanlig systemvariabel (gettext), till exempel "en_US:en".
 */
const OWN = ["URL", "TOKEN", "ENABLED", "LANGUAGE"];
// Namnet matchas så kort som möjligt, så att FEED_X_MASTODON_URL blir flödet X och fältet MASTODON_URL.
const FEED_KEY = new RegExp(`^FEED_([A-Z0-9][A-Z0-9_]*?)_(${[...OWN, ...SHARED].join("|")})$`);

const MIN_INTERVAL_MS = 60_000;

/** "900" (sekunder), "15m", "1h30m", "2d" → millisekunder. null om texten inte går att tolka. */
export function parseDuration(text: string): number | null {
  const t = text.trim().toLowerCase();
  if (/^\d+$/.test(t)) return Number(t) * 1000;
  const m = /^(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(t);
  if (!t || !m) return null;
  const [days, hours, minutes, seconds] = m.slice(1).map((v) => Number(v ?? 0));
  return (((days * 24 + hours) * 60 + minutes) * 60 + seconds) * 1000;
}

function flag(value: string | undefined): boolean {
  return /^(1|true|yes|ja|on)$/i.test(value?.trim() ?? "");
}

function httpUrl(value: string): URL | null {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:" ? url : null;
  } catch {
    return null;
  }
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  const problems = new Set<string>();
  const byName = new Map<string, Map<string, string>>();

  for (const [key, raw] of Object.entries(env)) {
    if (!key.startsWith("FEED_") || raw === undefined) continue;
    const m = FEED_KEY.exec(key);
    if (!m) {
      problems.add(`Okänd inställning ${key} (stavfel?)`);
      continue;
    }
    const fields = byName.get(m[1]) ?? new Map<string, string>();
    fields.set(m[2], raw.trim());
    byName.set(m[1], fields);
  }

  const feeds: FeedConfig[] = [];
  const paused: string[] = [];

  for (const envName of [...byName.keys()].sort()) {
    const fields = byName.get(envName)!;
    const prefix = `FEED_${envName}_`;
    const name = envName.toLowerCase();
    const enabled = fields.get("ENABLED") ?? "";
    if (enabled && !/^(1|true|yes|ja|on|0|false|no|nej|off)$/i.test(enabled)) {
      problems.add(`${prefix}ENABLED måste vara true eller false, inte "${enabled}"`);
    }
    if (enabled && !flag(enabled)) {
      paused.push(name);
      continue;
    }

    const own: string[] = [];
    const fail = (msg: string) => {
      own.push(msg);
      problems.add(msg);
    };
    /** Flödets eget värde, annars det globala. `source` är variabeln som gav värdet. */
    const pick = (field: string): { value?: string; source: string } => {
      const value = fields.get(field);
      if (value) return { value, source: prefix + field };
      const global = env[field]?.trim();
      return { value: global || undefined, source: global ? field : prefix + field };
    };

    const url = fields.get("URL");
    if (!url) fail(`${prefix}URL saknas`);
    else if (!httpUrl(url)) fail(`${prefix}URL är ingen http(s)-adress: "${url}"`);

    const token = fields.get("TOKEN");
    if (!token) fail(`${prefix}TOKEN saknas`);

    const instance = pick("MASTODON_URL");
    const instanceUrl = instance.value ? httpUrl(instance.value) : null;
    if (!instance.value) fail(`MASTODON_URL saknas (globalt eller som ${prefix}MASTODON_URL)`);
    else if (!instanceUrl || instanceUrl.pathname !== "/" || instanceUrl.search) {
      fail(`${instance.source} ska vara instansens adress, till exempel https://mastodon.nu`);
    }

    const duration = (field: string, fallback: string, min: number): number => {
      const { value = fallback, source } = pick(field);
      const ms = parseDuration(value);
      if (ms === null) fail(`${source} är ingen tid: "${value}" (till exempel 900, 15m eller 1h30m)`);
      else if (ms < min) fail(`${source} är för kort: minst ${min / 1000} s`);
      return ms ?? 0;
    };
    const integer = (field: string, fallback: string, min: number): number => {
      const { value = fallback, source } = pick(field);
      if (!/^\d+$/.test(value) || Number(value) < min) fail(`${source} måste vara ett heltal, minst ${min}: "${value}"`);
      return Number(value);
    };
    const oneOf = <T extends string>(field: string, allowed: readonly T[], fallback?: T): T | undefined => {
      const { value, source } = pick(field);
      if (value === undefined) return fallback;
      const lower = value.toLowerCase() as T;
      if (!allowed.includes(lower)) fail(`${source} måste vara ${allowed.join(", ")}: "${value}"`);
      return lower;
    };

    const intervalMs = duration("INTERVAL", "15m", MIN_INTERVAL_MS);
    const maxAgeMs = duration("MAX_AGE", "48h", 0);
    const maxPostsPerPoll = integer("MAX_POSTS_PER_POLL", "5", 1);
    const backfill = integer("BACKFILL", "0", 0);
    const visibility = oneOf("VISIBILITY", VISIBILITIES, "public")!;
    const quotePolicy = oneOf("QUOTE_POLICY", QUOTE_POLICIES);

    const lang = fields.get("LANGUAGE");
    const langMatch = lang ? /^([a-z]{2,3})(?:[-_][a-z0-9]{1,8})*$/i.exec(lang) : null;
    if (lang && !langMatch) fail(`${prefix}LANGUAGE ska vara en språkkod som sv eller en: "${lang}"`);

    const tpl = pick("TEMPLATE");
    // Ett \n i värdet blir en radbrytning, så att mallen går att skriva på en rad i .env.
    const template = (tpl.value ?? "{title}\n\n{link}").replaceAll("\\n", "\n");
    for (const [, field] of template.matchAll(/\{(\w+)\}/g)) {
      if (!TEMPLATE_FIELDS.includes(field)) {
        fail(`${tpl.source} har okänt fält {${field}}; giltiga är ${TEMPLATE_FIELDS.map((f) => `{${f}}`).join(", ")}`);
      }
    }

    if (own.length) continue;
    feeds.push({
      name,
      prefix,
      url: url!,
      token: token!,
      mastodonUrl: instanceUrl!.origin,
      intervalMs,
      visibility,
      language: langMatch?.[1].toLowerCase(),
      template,
      quotePolicy,
      maxAgeMs,
      maxPostsPerPoll,
      backfill,
    });
  }

  const targets = new Map<string, string>();
  for (const feed of feeds) {
    const target = `${feed.url}\n${feed.mastodonUrl}\n${feed.token}`;
    const other = targets.get(target);
    if (other) problems.add(`${other} och ${feed.prefix} postar samma flöde till samma konto`);
    else targets.set(target, feed.prefix);
  }

  if (feeds.length === 0 && paused.length === 0 && problems.size === 0) {
    problems.add("Inga flöden: ange minst FEED_<NAMN>_URL och FEED_<NAMN>_TOKEN");
  }

  const number = (name: string, fallback: string, min: number, max = Infinity): number => {
    const value = env[name]?.trim() || fallback;
    if (!/^\d+$/.test(value) || Number(value) < min || Number(value) > max) {
      problems.add(`${name} måste vara ett heltal mellan ${min} och ${max}: "${value}"`);
    }
    return Number(value);
  };
  const failNotifyAfter = number("FAIL_NOTIFY_AFTER", "3", 1);
  const statusPort = number("STATUS_PORT", "8080", 0, 65535);
  const signalUrl = env.SIGNAL_API_URL?.trim().replace(/\/+$/, "") ?? "";
  const signalNumber = env.SIGNAL_NUMBER?.trim() ?? "";
  if (signalUrl && !httpUrl(signalUrl)) problems.add(`SIGNAL_API_URL är ingen http(s)-adress: "${signalUrl}"`);
  if (Boolean(signalUrl) !== Boolean(signalNumber)) {
    problems.add("SIGNAL_API_URL och SIGNAL_NUMBER behövs båda för Signal-aviseringar");
  }
  if (problems.size) throw new ConfigError([...problems]);

  return {
    feeds,
    paused,
    dataDir: env.DATA_DIR?.trim() || "/data",
    dryRun: flag(env.DRY_RUN),
    once: flag(env.ONCE),
    debug: env.LOG_LEVEL?.trim().toLowerCase() === "debug",
    signalUrl,
    signalNumber,
    failNotifyAfter,
    statusPort,
  };
}
