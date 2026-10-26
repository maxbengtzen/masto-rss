/**
 * Hämtar och tolkar flöden.
 *
 * Hämtningen följer omdirigeringar själv, så att den kan säga till när en adress flyttat
 * permanent. Den skickar ETag/Last-Modified så att ett oförändrat flöde ger 304, har en total
 * timeout och en storleksgräns, och avkodar teckenkodningen ur BOM, Content-Type eller
 * XML-deklarationen. Själva tolkningen görs av feedsmith; här görs resultatet om till samma form
 * för RSS, Atom, RDF och JSON Feed.
 */
import { parseFeed } from "feedsmith";
import { USER_AGENT } from "./version.ts";

export const TIMEOUT_MS = 30_000;
export const MAX_BYTES = 5 * 1024 * 1024;
const MAX_REDIRECTS = 5;

export interface Validators {
  etag?: string;
  lastModified?: string;
}

export interface Redirect {
  /** Slutadressen efter alla omdirigeringar. */
  to: string;
  /** Sant om varje steg var 301 eller 308. */
  permanent: boolean;
}

export type FetchResult =
  | { status: "not-modified"; redirect?: Redirect }
  | { status: "ok"; text: string; url: string; etag?: string; lastModified?: string; redirect?: Redirect };

export class HttpError extends Error {
  status: number;
  /** Sekunder enligt Retry-After, om servern skickade det. */
  retryAfter?: number;

  constructor(status: number, statusText: string, retryAfter?: number) {
    super(`HTTP ${status} ${statusText}`.trim());
    this.name = "HttpError";
    this.status = status;
    this.retryAfter = retryAfter;
  }
}

function retryAfterSeconds(value: string | null): number | undefined {
  if (!value) return undefined;
  if (/^\d+$/.test(value.trim())) return Number(value);
  const date = Date.parse(value);
  return Number.isNaN(date) ? undefined : Math.max(0, Math.round((date - Date.now()) / 1000));
}

export async function fetchFeed(url: string, validators: Validators = {}): Promise<FetchResult> {
  const signal = AbortSignal.timeout(TIMEOUT_MS);
  const headers: Record<string, string> = {
    "User-Agent": USER_AGENT,
    Accept: "application/rss+xml, application/atom+xml, application/feed+json, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.8",
  };
  if (validators.etag) headers["If-None-Match"] = validators.etag;
  if (validators.lastModified) headers["If-Modified-Since"] = validators.lastModified;

  let current = url;
  let permanent = true;
  for (let hops = 0; ; hops++) {
    const res = await fetch(current, { headers, redirect: "manual", signal });
    const location = res.headers.get("location");
    if (res.status >= 300 && res.status < 400 && res.status !== 304 && location) {
      await res.body?.cancel();
      if (hops >= MAX_REDIRECTS) throw new Error(`Fler än ${MAX_REDIRECTS} omdirigeringar`);
      if (res.status !== 301 && res.status !== 308) permanent = false;
      current = new URL(location, current).href;
      continue;
    }

    const redirect = hops ? { to: current, permanent } : undefined;
    if (res.status === 304) {
      await res.body?.cancel();
      return { status: "not-modified", redirect };
    }
    if (!res.ok) {
      await res.body?.cancel();
      throw new HttpError(res.status, res.statusText, retryAfterSeconds(res.headers.get("retry-after")));
    }
    return {
      status: "ok",
      text: decodeBody(await readLimited(res), res.headers.get("content-type")),
      url: current,
      etag: res.headers.get("etag") ?? undefined,
      lastModified: res.headers.get("last-modified") ?? undefined,
      redirect,
    };
  }
}

async function readLimited(res: Response): Promise<Uint8Array> {
  const tooBig = () => new Error(`Flödet är större än ${MAX_BYTES / 1024 / 1024} MB`);
  if (Number(res.headers.get("content-length")) > MAX_BYTES) {
    await res.body?.cancel();
    throw tooBig();
  }
  const chunks: Uint8Array[] = [];
  let size = 0;
  for await (const chunk of res.body ?? []) {
    size += chunk.byteLength;
    if (size > MAX_BYTES) throw tooBig(); // att lämna loopen avbryter nedladdningen
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

/** Avkodar svaret: BOM först, sedan Content-Type, sedan XML-deklarationen, annars UTF-8. */
export function decodeBody(bytes: Uint8Array, contentType: string | null): string {
  let label: string;
  if (bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf) label = "utf-8";
  else if (bytes[0] === 0xff && bytes[1] === 0xfe) label = "utf-16le";
  else if (bytes[0] === 0xfe && bytes[1] === 0xff) label = "utf-16be";
  else {
    const fromHeader = /charset\s*=\s*"?([\w.:-]+)/i.exec(contentType ?? "")?.[1];
    const head = new TextDecoder("latin1").decode(bytes.subarray(0, 512));
    const fromXml = /^\s*<\?xml[^>]*\bencoding\s*=\s*["']([\w.:-]+)["']/i.exec(head)?.[1];
    label = fromHeader ?? fromXml ?? "utf-8";
  }
  try {
    return new TextDecoder(label).decode(bytes);
  } catch {
    return new TextDecoder("utf-8").decode(bytes); // okänd teckenkodning
  }
}

/** Spårningsparametrar som inte ska påverka om två länkar räknas som samma. */
const TRACKING = /^(utm_\w+|fbclid|gclid|dclid|gbraid|wbraid|msclkid|yclid|mc_cid|mc_eid|_hsenc|_hsmi|igshid)$/i;

/**
 * Länken som nyckel: utan schema, `www.`, spårningsparametrar, fragment och avslutande
 * snedstreck, och med övriga parametrar i bokstavsordning. Två länkar som bara skiljer sig så
 * räknas som samma post.
 */
export function normalizeLink(link: string): string {
  const raw = link.trim();
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return raw;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return raw;
  const params = [...url.searchParams]
    .filter(([key]) => !TRACKING.test(key))
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  const query = params.length ? `?${new URLSearchParams(params)}` : "";
  return url.host.replace(/^www\./, "") + url.pathname.replace(/\/+$/, "") + query;
}

const ENTITIES: Record<string, string> = {
  amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " ", ensp: " ", emsp: " ",
  thinsp: " ", shy: "", hellip: "…", ndash: "–", mdash: "—", lsquo: "‘", rsquo: "’", sbquo: "‚",
  ldquo: "“", rdquo: "”", bdquo: "„", laquo: "«", raquo: "»", lsaquo: "‹", rsaquo: "›", bull: "•",
  middot: "·", copy: "©", reg: "®", trade: "™", euro: "€", pound: "£", deg: "°", times: "×",
  divide: "÷", plusmn: "±", frac12: "½", frac14: "¼", frac34: "¾", sect: "§", para: "¶",
  aring: "å", Aring: "Å", auml: "ä", Auml: "Ä", ouml: "ö", Ouml: "Ö", aelig: "æ", AElig: "Æ",
  oslash: "ø", Oslash: "Ø", eacute: "é", Eacute: "É", egrave: "è", Egrave: "È", ecirc: "ê",
  aacute: "á", agrave: "à", iacute: "í", oacute: "ó", uacute: "ú", uuml: "ü", Uuml: "Ü",
  ntilde: "ñ", ccedil: "ç", szlig: "ß",
};

/** HTML-entiteter till tecken. Okända namngivna entiteter lämnas som de är. */
export function decodeEntities(text: string): string {
  return text.replace(/&(#x[0-9a-f]+|#\d+|[a-z][a-z0-9]*);/gi, (whole, name: string) => {
    if (name[0] !== "#") return ENTITIES[name] ?? whole;
    const code = name[1] === "x" || name[1] === "X" ? parseInt(name.slice(2), 16) : parseInt(name.slice(1), 10);
    return code > 0 && code <= 0x10ffff ? String.fromCodePoint(code) : whole;
  });
}

const BLOCK_TAG = /<\/?(?:p|div|br|hr|li|ul|ol|h[1-6]|blockquote|pre|table|tr|td|th|section|article|figure|figcaption|header|footer)\b[^>]*>/gi;

/** HTML till en rad text: taggar bort, entiteter avkodade, blanktecken hopslagna. */
export function htmlToText(html: string): string {
  const stripped = html
    .replace(/<(script|style)\b[\s\S]*?<\/\1\s*>/gi, " ")
    .replace(/<!--[\s\S]*?-->/g, " ")
    .replace(BLOCK_TAG, " ")
    .replace(/<[^>]*>/g, "");
  return decodeEntities(stripped).replace(/\s+/g, " ").trim();
}

export interface FeedItem {
  /** Nycklar för dubblettkontrollen: "g:" + GUID och "l:" + normaliserad länk. */
  keys: string[];
  title: string;
  link?: string;
  summary: string;
  author?: string;
  /** Publiceringstiden i ms, om flödet anger en giltig. */
  published?: number;
}

export interface ParsedFeed {
  title?: string;
  /** Flödets språk som ISO 639-kod, till exempel "sv". */
  language?: string;
  items: FeedItem[];
  /** Poster utan både id och länk. De går inte att känna igen och hoppas över. */
  skipped: number;
}

interface RawItem {
  id?: string;
  link?: string;
  title?: string;
  summary?: string;
  author?: string;
  date?: string;
}

function toItem(raw: RawItem, base: string): FeedItem | null {
  const id = raw.id?.trim();
  let link = raw.link?.trim() || undefined;
  if (link) {
    try {
      link = new URL(link, base).href;
    } catch {
      // behåll länken som den står
    }
  }
  const keys: string[] = [];
  if (id) keys.push(`g:${id}`);
  if (link) keys.push(`l:${normalizeLink(link)}`);
  if (!keys.length) return null;
  const published = raw.date ? Date.parse(raw.date) : NaN;
  return {
    keys,
    title: htmlToText(raw.title ?? ""),
    link,
    summary: htmlToText(raw.summary ?? ""),
    author: raw.author ? htmlToText(raw.author) || undefined : undefined,
    published: Number.isNaN(published) ? undefined : published,
  };
}

function languageCode(value: string | undefined): string | undefined {
  return /^([a-z]{2,3})(?:[-_][a-z0-9]{1,8})*$/i.exec(value?.trim() ?? "")?.[1].toLowerCase();
}

/** Tolkar ett flöde i valfritt format. `base` är adressen som relativa länkar utgår från. */
export function parse(text: string, base: string): ParsedFeed {
  const result = parseFeed(text);
  let title: string | undefined;
  let language: string | undefined;
  let raws: RawItem[];

  switch (result.format) {
    case "rss": {
      const { feed } = result;
      title = feed.title;
      language = feed.language ?? feed.dc?.languages?.[0];
      raws = (feed.items ?? []).map((i) => ({
        id: i.guid?.value,
        link: i.link,
        title: i.title,
        summary: i.description ?? i.content?.encoded,
        author: i.dc?.creators?.[0] ?? i.authors?.[0]?.name ?? i.authors?.[0]?.email,
        date: i.pubDate ?? i.dc?.dates?.[0],
      }));
      break;
    }
    case "atom": {
      const { feed } = result;
      title = feed.title?.value;
      language = feed.xml?.lang;
      raws = (feed.entries ?? []).map((e) => ({
        id: e.id,
        link: (e.links?.find((l) => !l.rel || l.rel === "alternate") ?? e.links?.[0])?.href,
        title: e.title?.value,
        summary: e.summary?.value ?? e.content?.value,
        author: e.authors?.[0]?.name,
        date: e.published ?? e.updated,
      }));
      break;
    }
    case "rdf": {
      const { feed } = result;
      title = feed.title;
      language = feed.dc?.languages?.[0];
      raws = (feed.items ?? []).map((i) => ({
        id: i.rdf?.about,
        link: i.link,
        title: i.title,
        summary: i.description ?? i.content?.encoded,
        author: i.dc?.creators?.[0],
        date: i.dc?.dates?.[0],
      }));
      break;
    }
    case "json": {
      const { feed } = result;
      title = feed.title;
      language = feed.language;
      raws = (feed.items ?? []).map((i) => ({
        id: i.id,
        link: i.url ?? i.external_url,
        title: i.title,
        summary: i.summary ?? i.content_text ?? i.content_html,
        author: i.authors?.[0]?.name,
        date: i.date_published ?? i.date_modified,
      }));
      break;
    }
  }

  const items: FeedItem[] = [];
  for (const raw of raws) {
    const item = toItem(raw, base);
    if (item) items.push(item);
  }
  return {
    title: title ? htmlToText(title) || undefined : undefined,
    language: languageCode(language),
    items,
    skipped: raws.length - items.length,
  };
}
