/**
 * Spelar in TypeScript-versionens svar som golden-filer i testdata/golden/, inför omskrivningen
 * till Go. Tillfälligt: tas bort tillsammans med resten av TypeScript-koden.
 *
 *   TZ=Europe/Stockholm node scripts/golden.ts
 *   TZ=Europe/Stockholm FIXTURES=testdata/local/fixtures GOLDEN=testdata/local/golden node scripts/golden.ts
 *
 * Det andra sättet spelar bara in flöden och inläggens text för flöden som inte versionshanteras
 * (till exempel sparade riktiga flöden). Kör det i samma Node som produktionen (node:26-alpine), så
 * att ICU och Unicode är desamma.
 */
import { spawn } from "node:child_process";
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ConfigError, loadConfig, parseDuration } from "../src/config.ts";
import { decodeBody, decodeEntities, htmlToText, normalizeLink, parse } from "../src/feed.ts";
import { defuseMentions, renderStatus, statusLength, type Limits } from "../src/format.ts";
import { postId } from "../src/poll.ts";
import { Store } from "../src/store.ts";
import { ago, daysAgo } from "../src/status.ts";

const ROOT = new URL("..", import.meta.url).pathname;
const FIXTURES = join(ROOT, process.env.FIXTURES ?? "testdata/fixtures");
const GOLDEN = join(ROOT, process.env.GOLDEN ?? "testdata/golden");
const LOCAL = process.env.FIXTURES !== undefined;

function save(name: string, value: unknown): void {
  writeFileSync(join(GOLDEN, name), `${JSON.stringify(value, null, 2)}\n`);
  console.log(`skrev ${name}`);
}

const describe = (err: unknown) => (err instanceof Error ? `${err.name}: ${err.message}` : String(err));

// --- Flöden ---------------------------------------------------------------------------------

const feeds: Record<string, unknown> = {};
const parsedTexts: Array<{ title: string; summary: string; author?: string; link?: string; feed?: string }> = [];
for (const file of readdirSync(FIXTURES).sort()) {
  const bytes = readFileSync(join(FIXTURES, file));
  const text = decodeBody(bytes, file.endsWith(".json") ? "application/feed+json" : "application/rss+xml");
  const base = `https://example.com/feeds/${file}`;
  try {
    const feed = parse(text, base);
    feeds[file] = feed;
    for (const item of feed.items) parsedTexts.push({ ...item, feed: feed.title });
  } catch (err) {
    feeds[file] = { error: describe(err), cause: err instanceof Error && err.cause instanceof Error ? describe(err.cause) : undefined };
  }
}
save("feeds.json", feeds);

// --- Inläggens text -------------------------------------------------------------------------

const crafted = [
  "",
  "Rubrik",
  "Tack @anna och @bo@example.social! Mejla info@example.com, https://medium.com/@skribent/inlagg, a=@b, x/@y",
  "@först @åsa @_under @9 @@dubbel @ ensam @",
  "👨‍👩‍👧 Familj 🇸🇪🇺🇸🇫 flaggor 👍🏽 hudton 1️⃣ #️⃣ ❤️ e\u0301 o\u0308\u0332 한국어 \u1100\u1161\u11a8 क्षि ไทย",
  "Läs https://example.com/en/mycket/lang/adress?utm_source=rss&x=1 och http://a.se/b, sedan https://exempel.se/å<b>",
  "Ord ".repeat(60),
  "x".repeat(600),
  "Mycket lång rubrik med många ord som inte får plats i ett kort inlägg på Mastodon alls, verkligen inte, inte ens nästan",
  "Rader\n\n\n\nmed\ttabbar   och    mellanslag  \n  ",
  "\u00a0Hårt\u00a0mellanslag\u2003och\u3000annat\u2028radbrytning\ufeffslut ",
  "😀".repeat(30),
  "🏳️‍🌈🏴‍☠️🧑🏿‍🤝‍🧑🏻👩🏽‍💻",
  "\r\n\r\nCRLF\r\nrad",
  "a\u200db\u200d\u200dc\u0300\u0300",
  "https://example.com/" + "a".repeat(100),
  "Slut med länk https://example.com/x",
  "[länk](https://example.com/a) <https://example.com/b> \"https://example.com/c\"",
];
const templates = [
  "{title}\n\n{link}",
  "{feed}: {title} ({author})\n{summary}\n{link}",
  "{title}\n\n{summary}\n\n{link}",
  "{summary}",
  "{title} {title} {link} {link}",
  "  {title}  \n\n\n\n{link}  ",
  "@{author} {title}\n{summary}",
  "{link}",
  "",
];
const limits: Limits[] = [
  { maxCharacters: 500, charactersPerUrl: 23 },
  { maxCharacters: 100, charactersPerUrl: 23 },
  { maxCharacters: 60, charactersPerUrl: 23 },
  { maxCharacters: 60 },
  { maxCharacters: 40, charactersPerUrl: 23 },
  { maxCharacters: 25, charactersPerUrl: 23 },
  { maxCharacters: 10, charactersPerUrl: 23 },
  { maxCharacters: 1, charactersPerUrl: 23 },
  { maxCharacters: 0, charactersPerUrl: 23 },
];

const format: Array<Record<string, unknown>> = [];
const fieldSets = [
  ...parsedTexts.slice(0, 40),
  ...crafted.map((s, i) => ({ title: s, summary: crafted[(i + 3) % crafted.length], author: crafted[(i + 1) % crafted.length], link: i % 3 ? `https://example.com/${i}` : undefined, feed: crafted[(i + 5) % crafted.length] })),
];
for (const [i, fields] of fieldSets.entries()) {
  for (const [j, template] of templates.entries()) {
    // Alla kombinationer blir för många; varje fältuppsättning får tre mallar och tre gränser.
    if ((i + j) % 3 !== 0) continue;
    for (const [k, lim] of limits.entries()) {
      if ((i + j + k) % 3 !== 0) continue;
      const out = renderStatus(template, fields, lim);
      format.push({ template, fields, limits: lim, out, length: statusLength(out, lim) });
    }
  }
}
const lengths = [...crafted, ...parsedTexts.map((p) => p.summary)].map((text) => ({
  text,
  length: statusLength(text, { maxCharacters: 500, charactersPerUrl: 23 }),
  graphemes: statusLength(text, { maxCharacters: 500 }),
  defused: defuseMentions(text),
}));
save("format.json", { render: format, lengths });
if (LOCAL) process.exit(0);

// --- Konfiguration --------------------------------------------------------------------------

const durations = ["900", "15m", "1h30m", "2d", " 45S ", "0", "", "15 min", "m15", "1d2h3m4s", "1h1h", "01m", "1M", "10s", "59s", "60", "1h 30m", "3600000000000", "1.5h", "-5m", "\u00a015m\u00a0", "１５m"];
const configs: Array<Array<[string, string]>> = [
  [["MASTODON_URL", "https://mastodon.example/"], ["FEED_NEWS_URL", "https://example.com/feed/"], ["FEED_NEWS_TOKEN", "token-1"]],
  [
    ["MASTODON_URL", "https://mastodon.example/"], ["FEED_NEWS_URL", "https://example.com/feed/"], ["FEED_NEWS_TOKEN", "token-1"],
    ["INTERVAL", "30m"], ["VISIBILITY", "unlisted"], ["FEED_OTHER_URL", "https://example.org/rss"], ["FEED_OTHER_TOKEN", "token-2"],
    ["FEED_OTHER_INTERVAL", "1h"], ["FEED_OTHER_MASTODON_URL", "https://other.example"], ["FEED_OTHER_LANGUAGE", "sv-SE"],
    ["FEED_OTHER_QUOTE_POLICY", "Nobody"], ["DRY_RUN", "1"], ["ONCE", "true"], ["DATA_DIR", "/tmp/x"], ["LOG_LEVEL", " DEBUG "],
  ],
  [["MASTODON_URL", "https://mastodon.example"], ["FEED_FASTIGHETS_SVERIGE_URL", "https://example.com/feed"], ["FEED_FASTIGHETS_SVERIGE_TOKEN", "t"], ["FEED_FASTIGHETS_SVERIGE_MAX_POSTS_PER_POLL", "2"]],
  [["MASTODON_URL", "https://mastodon.example/"], ["FEED_NEWS_URL", "https://example.com/feed/"], ["FEED_NEWS_TOKEN", "token-1"], ["LANGUAGE", "en_US:en"], ["TEMPLATE", "{title}\\n{link} #nyheter"], ["MAX_AGE", "0"]],
  [["MASTODON_URL", "https://mastodon.example/"], ["FEED_NEWS_URL", "https://example.com/feed/"], ["FEED_NEWS_TOKEN", "token-1"], ["FEED_OLD_ENABLED", "false"], ["FEED_OLD_URL", "inte en adress"]],
  [
    ["FEED_NEWS_URL", "ftp://example.com/feed"], ["FEED_NEWS_TEMPLTE", "{title}"], ["FEED_OTHER_URL", "https://example.org/"],
    ["FEED_OTHER_INTERVAL", "30"], ["FEED_OTHER_VISIBILITY", "everyone"], ["FEED_OTHER_TEMPLATE", "{title} {body}"], ["FEED_OTHER_MAX_POSTS_PER_POLL", "0"],
  ],
  [["MASTODON_URL", "https://mastodon.example/"], ["FEED_NEWS_URL", "https://example.com/feed/"], ["FEED_NEWS_TOKEN", "token-1"], ["FEED_NEWS_ENABLED", "kanske"]],
  [["MASTODON_URL", "https://mastodon.example/@konto"], ["FEED_NEWS_URL", "https://example.com/feed/"], ["FEED_NEWS_TOKEN", "token-1"]],
  [["MASTODON_URL", "https://mastodon.example/"], ["FEED_NEWS_URL", "https://example.com/feed/"], ["FEED_NEWS_TOKEN", "token-1"], ["FEED_COPY_URL", "https://example.com/feed/"], ["FEED_COPY_TOKEN", "token-1"]],
  [["MASTODON_URL", "https://mastodon.example"]],
  [],
  [
    ["MASTODON_URL", "HTTPS://Mastodon.Example:443/"], ["FEED_A_URL", " https://example.com/a "], ["FEED_A_TOKEN", " t "], ["FEED_A_ENABLED", "JA"],
    ["FEED_A_LANGUAGE", "SV_se"], ["FEED_A_BACKFILL", "3"], ["FEED_A_TEMPLATE", "{summary} {author} {feed}"], ["FEED_B_URL", "http://example.com/b?x=1"],
    ["FEED_B_TOKEN", "t2"], ["FEED_B_MASTODON_URL", "https://m.example/?x"], ["FEED_B_VISIBILITY", "DIRECT"], ["FEED_B_QUOTE_POLICY", "alla"],
    ["FEED_B_LANGUAGE", "svenska"], ["FEED_B_MAX_AGE", "fem"], ["FEED_B_BACKFILL", "-1"], ["FEED_C_ENABLED", "nej"], ["FEED_", "x"], ["FEED_X_Y", "z"],
    ["FAIL_NOTIFY_AFTER", "0"], ["STATUS_PORT", "70000"], ["SIGNAL_API_URL", "http://signal:8080//"],
  ],
  [["MASTODON_URL", "https://mastodon.example"], ["FEED_A_URL", "https://example.com/a"], ["FEED_A_TOKEN", "t"], ["SIGNAL_NUMBER", "+46700000000"], ["STATUS_PORT", ""], ["FAIL_NOTIFY_AFTER", " 7 "], ["DATA_DIR", "  "]],
  [["MASTODON_URL", "https://mastodon.example"], ["FEED_A_URL", "https://example.com/a"], ["FEED_A_TOKEN", "t"], ["SIGNAL_API_URL", "ftp://x"], ["SIGNAL_NUMBER", "1"], ["FEED_A_INTERVAL", "59s"], ["INTERVAL", "1m"], ["FEED_A_MAX_POSTS_PER_POLL", "1"]],
  [["FEED_A_ENABLED", "off"], ["FEED_B_ENABLED", "0"]],
  [["MASTODON_URL", "https://xn--mstodon-5wa.example"], ["FEED_A_URL", "https://exämple.com/flöde"], ["FEED_A_TOKEN", "t"], ["FEED_A_MASTODON_URL", "https://[::1]:8443"], ["TEMPLATE", "{title}{link}{constructor}"]],
];
const configResults = configs.map((pairs) => {
  const env = Object.fromEntries(pairs);
  try {
    return { env: pairs, config: loadConfig(env) };
  } catch (err) {
    return { env: pairs, problems: err instanceof ConfigError ? err.problems : [describe(err)] };
  }
});
save("config.json", { durations: durations.map((text) => ({ text, ms: parseDuration(text) })), configs: configResults });

// --- Länkar, entiteter, HTML, teckenkodning, datum, tid ---------------------------------------

const links = [
  "https://www.example.com/nyheter/en-post/?utm_source=rss&utm_medium=feed",
  "http://example.com/nyheter/en-post#kommentarer",
  "  https://EXAMPLE.com/nyheter/en-post  ",
  "https://example.com/?p=12&fbclid=abc&a=1",
  "https://example.com:8443/x/",
  "https://example.com:443/x/",
  "http://example.com:80/x//",
  "tag:example.com,2026:1",
  "inte en adress",
  "https://example.com/a?b=1&a=2&B=3&a=1&c",
  "https://example.com/a?q=hej+du&r=%C3%A5&s=a%2Bb&t=~*'()!&u=%zz&v=å",
  "https://example.com/a?=x&&y=&z",
  "https://example.com/å/ä ö?x=ö#frag",
  "https://exämple.com/flöde",
  "https://ÉXAMPLE.com/",
  "https://example.com/a/./b/../c",
  "https://example.com/%7Euser/%41",
  "HTTPS://WWW.EXAMPLE.COM",
  "https://www.www.example.com/x",
  "https://wwwexample.com/x",
  "https://user:pass@example.com/x",
  "https://example.com?utm_source=x",
  "https://example.com/x?UTM_SOURCE=x&Fbclid=1&gclid=2&dclid=&gbraid&wbraid=1&msclkid=1&yclid=1&mc_cid=1&mc_eid=1&_hsenc=1&_hsmi=1&igshid=1&utm_=1&utm_x_y=1",
  "https://127.0.0.1:8080/x",
  "https://0x7f.1/x",
  "https://[2001:db8::1]:443/x",
  "https://[2001:DB8:0:0:0:0:0:1]/",
  "http://example.com\\a\\b",
  "https:example.com/x",
  "https://example.com/a b",
  "https://example.com/a\tb\nc",
  "mailto:anna@example.com",
  "ftp://example.com/x/",
  "https://example.com/x?a=1#",
  "https://example.com/{x}|^`",
  "https://example.com/%E2%80%93",
  "https://example.com./x",
  "http://",
  "https://exa mple.com/",
  "//example.com/x",
  "/relativ/väg",
  "javascript:alert(1)",
  "https://example.com/x?a=%41&a=%41",
  "https://example.com/?a=1&a=2&a=0",
  "https://xn--exmple-cua.com/",
  "https://EXAMPLE.com/Väg/?Q=Ä",
];
const bases = ["https://example.com/feeds/a/feed.xml", "http://example.org", "https://example.com/feed/?x=1#y"];
const relative = ["/a", "b", "../c", "?q=1", "#frag", "//other.example/x", "https://abs.example/y", "  /trim  ", "", "javascript:x", "tag:a,b", "/a b", "/å", "http:/x", "mailto:x@y", "\\back", "././d/../e"];
const htmls = [
  "<p>Första <b>stycket</b>&nbsp;här.</p><p>Andra<br/>raden &lt;b&gt;</p><script>alert(1)</script><!-- x -->",
  "  \n ",
  "<SCRIPT type='x'>a</script >b<style>c</STYLE>d<scripts>e</scripts><script>f",
  "<pre>x</pre><prex>y<p\nclass=a>z<blockquote>q</blockquote><h7>h</h7><H2>H</H2>",
  "a<!-- b --><!-- c",
  "Hej &amp; v&#228;lkommen &#x2013; &aring;r&nbsp;2026 &okand; &#X41; &#0; &#1114112; &#xD800; &AMP; &Aring; &hellip;&shy;",
  "&amp;amp; &lt;p&gt;inte tagg&lt;/p&gt; &#38;#228;",
  "Rad1\r\nRad2\u2028Rad3\u00a0\u3000slut\ufeff",
  "<a href=\"x\">länk</a> & <img src=x> < 5 > 3",
  "<p>a</p>\n\n<p>b</p>",
  "<div><figure><figcaption>Bild</figcaption></figure></div>text",
  "<br><BR/><br />",
  "&#128512; &#x1F600; &#55357;&#56832;",
];
const bodies: Array<{ hex: string; contentType: string | null }> = [
  { hex: Buffer.from('<?xml version="1.0" encoding="ISO-8859-1"?><rss>\xe5\xe4\xf6</rss>', "latin1").toString("hex"), contentType: "application/rss+xml" },
  { hex: Buffer.from("<rss>\xe5\x80\x9f</rss>", "latin1").toString("hex"), contentType: "text/xml; charset=windows-1252" },
  { hex: "efbbbf" + Buffer.from("<rss>å</rss>").toString("hex"), contentType: "text/xml; charset=iso-8859-1" },
  { hex: Buffer.from("<rss>å</rss>").toString("hex"), contentType: "text/xml; charset=nonsens" },
  { hex: Buffer.from("<rss>å</rss>").toString("hex"), contentType: null },
  { hex: "fffe" + Buffer.from("<rss>å</rss>", "utf16le").toString("hex"), contentType: "text/xml; charset=utf-8" },
  { hex: "feff" + Buffer.from("<rss>å</rss>", "utf16le").swap16().toString("hex"), contentType: null },
  { hex: Buffer.from("<rss>å</rss>", "utf16le").toString("hex"), contentType: "text/xml; charset=UTF-16LE" },
  { hex: Buffer.from("<rss>\xe5</rss>", "latin1").toString("hex"), contentType: 'text/xml; Charset = "Latin1"' },
  { hex: Buffer.from("<rss>\xe5</rss>", "latin1").toString("hex"), contentType: "text/xml; charset=us-ascii" },
  { hex: Buffer.from("  <?xml version='1.0' encoding='iso-8859-1'?><rss>\xe5</rss>", "latin1").toString("hex"), contentType: "text/xml; charset=" },
  { hex: "3c7273733ee2823c2f7273733e" /* avbruten UTF-8 */, contentType: null },
  { hex: "3c7273733ec3283cf09f98ffc0afeda080e282ac3c2f7273733e", contentType: "text/xml" },
  { hex: Buffer.from("<rss>\xa4</rss>", "latin1").toString("hex"), contentType: "text/xml; charset=iso-8859-15" },
  { hex: Buffer.from("<rss>\xe5</rss>", "latin1").toString("hex"), contentType: "text/xml; charset=ISO_8859-1:1987" },
  { hex: "", contentType: null },
  { hex: "ef", contentType: null },
  { hex: "efbbbf", contentType: null },
];
const dates = [
  "Mon, 26 Oct 2026 08:00:00 +0100", "Mon, 09 Nov 2026 20:00:00 GMT", "Mon, 09 Nov 2026 08:02:00 +0000", "Tue, 27 Oct 2026 08:00:00 GMT",
  "2026-10-26T07:30:00Z", "2026-10-26T07:30:00", "2026-10-26T07:30", "2026-10-26", "2026-10", "2026", "2026-10-26T07:30:00.123456Z",
  "2026-10-26T07:30:00+02:00", "2026-10-26T07:30:00+0200", "2026-10-26T24:00:00Z", "2026-10-26T24:00:01Z", "2026-10-26T7:30:00Z",
  "2026-10-26 07:30:00", "2026-10-26 07:30:00 +0100", "2026/10/26 07:30", "10/26/2026", "26/10/2026", "26 Oct 2026", "Oct 26 2026",
  "Oct 26", "26 October 2026 7:30 PM", "26 okt 2026", "Monday, 26-Oct-26 08:00:00 GMT", "Mon, 26 Oct 26 08:00:00 EST", "Mon, 26 Oct 2026 08:00:00 PDT",
  "Mon, 26 Oct 2026 08:00:00 CEST", "Mon, 26 Oct 2026 08:00:00 UT", "Mon, 26 Oct 2026 08:00:00 Z", "Mon, 26 Oct 2026 08:00 +01:00",
  "Mon, 26 Oct 2026 08:00:00 GMT+0100", "Mon, 26 Oct 2026 08:00:00 GMT-5", "Mon, 26 Oct 2026 08:00:00 (CET)", "trasigt datum", "",
  "+002026-10-26T00:00:00Z", "-000001-01-01T00:00:00Z", "2026-02-30", "2026-13-01", "Feb 30 2026", "2026-10-26T12:00:00.5Z",
  "1700000000", "@1700000000", "2026-10-26T07:30:00.000+01:00", "Mon Oct 26 2026 08:00:00 GMT+0100 (Central European Standard Time)",
  "Sun, 29 Mar 2026 02:30:00", "Sun, 25 Oct 2026 02:30:00", "12:00", "Mon, 26 Oct 2026 12:00:00 am", "Mon, 26 Oct 2026 12:00 pm",
  "2026-10-26T07:30:00z", "2026-10-26t07:30:00Z", "  2026-10-26T07:30:00Z  ", "26.10.2026", "2026.10.26", "Okt 26 2026",
  "Mon, 26 Oct 2026 08:00:00 +01", "Mon, 26 Oct 2026 08:00:00 +1", "Mon, 26 Oct 2026 08:00:00 +12345", "2026-10-26T07:30:60Z",
  "Thu, 01 Jan 1970 00:00:00 GMT", "Sat, 13 Sep 275760 00:00:00 GMT", "Sat, 14 Sep 275760 00:00:00 GMT", "49-1-1", "50-1-1", "0-1-1",
  "June 2026", "2026 June 5", "5 June 2026 10.30", "Mon, 26 Oct 2026 08:00:00.250 GMT", "Mon, 26 Oct 2026 8:0:0 GMT", "2026-10-26T07:30:00 Z",
];
const now = Date.UTC(2026, 9, 26, 12);
const agos = [-5_000, 20_000, 59_999, 60_000, 12 * 60_000, 3599_999, 3 * 3600_000, 23 * 3600_000 + 3599_999, 26 * 3600_000, 48 * 3600_000, 80 * 3600_000, 400 * 86_400_000];
const dayDates = ["2026-10-26", "2026-10-25", "2026-05-12", "2026-10-27", "2026-10-26T23:30:00Z", "2025-10-26", "trasigt"];
const nowsForDays = [Date.UTC(2026, 9, 26, 12), Date.UTC(2026, 9, 26, 22, 30), Date.UTC(2026, 2, 29, 0, 30)];
save("text.json", {
  normalizeLink: links.map((link) => ({ link, key: normalizeLink(link) })),
  resolve: bases.flatMap((base) =>
    [...relative, ...links].map((link) => {
      try {
        return { base, link, href: new URL(link, base).href };
      } catch {
        return { base, link, href: null };
      }
    }),
  ),
  decodeEntities: htmls.map((html) => ({ html, text: decodeEntities(html) })),
  htmlToText: htmls.map((html) => ({ html, text: htmlToText(html) })),
  decodeBody: bodies.map((b) => ({ ...b, text: decodeBody(Buffer.from(b.hex, "hex"), b.contentType) })),
  dates: dates.map((text) => {
    const ms = Date.parse(text);
    return { text, ms: Number.isNaN(ms) ? null : ms };
  }),
  ago: agos.map((delta) => ({ delta, text: ago(now - delta, now) })),
  daysAgo: nowsForDays.flatMap((n) => dayDates.map((date) => ({ now: n, date, text: daysAgo(date, n, "Europe/Stockholm") }))),
  postId: ["g:1", "l:example.com/a", "g:https://example.com/?p=3", "g:åäö 😀"].map((key) => ({ feed: "news", key, id: postId("news", key) })),
});

// --- Databasens schema --------------------------------------------------------------------

{
  const store = new Store(":memory:");
  save("schema.json", {
    userVersion: (store.db.prepare("pragma user_version").get() as { user_version: number }).user_version,
    objects: store.db.prepare("select type, name, tbl_name, sql from sqlite_schema order by name").all(),
  });
  store.close();
}

// --- Hela programmet mot en låtsad server ---------------------------------------------------

/** Poster nummer from..to, nyast först, en timme mellan varje, med den nyaste `newest`. */
function rss(from: number, to: number, newest: number, path: string): string {
  const items: string[] = [];
  for (let n = to; n >= from; n--) {
    items.push(
      `<item><title>Post ${n} från ${path} &amp; @anna</title><link>https://example.com${path}/post-${n}/?utm_source=rss</link>` +
        `<guid isPermaLink="false">https://example.com${path}/?p=${n}</guid><pubDate>${new Date(newest - (to - n) * 3600_000).toUTCString()}</pubDate>` +
        `<description><![CDATA[<p>Sammanfattning ${n}</p>]]></description></item>`,
    );
  }
  return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>Flöde ${path}</title><language>sv-SE</language>${items.join("\n")}</channel></rss>`;
}

const posts: Array<{ token?: string; key?: string; body: unknown }> = [];
const signals: string[] = [];
let base = "";
let postCount = 0;

async function readBody(req: IncomingMessage): Promise<string> {
  let body = "";
  for await (const chunk of req) body += chunk;
  return body;
}

const server = createServer(async (req, res) => {
  const send = (status: number, body: unknown, headers: Record<string, string> = {}) =>
    res.writeHead(status, { "content-type": "application/json", ...headers }).end(JSON.stringify(body));
  const token = req.headers.authorization?.replace(/^Bearer /, "");
  const accounts: Record<string, { id: string; acct: string; followers: number; scopes: string[]; bot: boolean }> = {
    bra: { id: "1", acct: "alfa", followers: 12345, scopes: ["read", "write"], bot: true },
    bra3: { id: "3", acct: "delta", followers: 0, scopes: ["write:statuses"], bot: false },
    las: { id: "2", acct: "gamma", followers: 7, scopes: ["read"], bot: true },
  };
  const account = token ? accounts[token] : undefined;
  const path = (req.url ?? "").split("?")[0];
  switch (`${req.method} ${path}`) {
    case "GET /old":
      return res.writeHead(301, { location: "/feed-d" }).end();
    case "GET /feed-a":
    case "GET /feed-c":
    case "GET /feed-d":
      return res.writeHead(200, { "content-type": "application/rss+xml; charset=utf-8" }).end(rss(1, 3, Date.now() - 60_000, path));
    case "GET /broken":
      return res.writeHead(500, "Internal Server Error").end("trasigt");
    case "GET /api/v2/instance":
      return send(200, { version: "4.7.3", api_versions: { mastodon: 11 }, configuration: { statuses: { max_characters: 500, characters_reserved_per_url: 23 } } });
    case "GET /api/v1/apps/verify_credentials":
      return account ? send(200, { name: "test", scopes: account.scopes }) : send(401, { error: "The access token is invalid" });
    case "GET /api/v1/accounts/verify_credentials":
      if (!account) return send(401, { error: "The access token is invalid" });
      return send(
        200,
        { id: account.id, acct: account.acct, url: `${base}/@${account.acct}`, followers_count: account.followers, statuses_count: 10, last_status_at: new Date().toLocaleDateString("sv-SE", { timeZone: "Europe/Stockholm" }), bot: account.bot },
        token === "bra3" ? { deprecation: "@1700000000" } : {},
      );
    case "POST /api/v1/statuses": {
      const body = JSON.parse(await readBody(req));
      if (!account) return send(401, { error: "The access token is invalid" });
      if (String(body.status).startsWith("Post 2 ")) return send(422, { error: "Validation failed: Text character limit of 500 exceeded" });
      posts.push({ token, key: req.headers["idempotency-key"] as string | undefined, body });
      postCount++;
      return send(200, { id: String(postCount), url: `${base}/@${account.acct}/${postCount}` });
    }
    case "POST /v2/send":
      signals.push(JSON.parse(await readBody(req)).message);
      return send(201, {});
    default:
      return send(404, { error: "Not found" });
  }
});
await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
const port = (server.address() as AddressInfo).port;
base = `http://127.0.0.1:${port}`;

const root = mkdtempSync(join(tmpdir(), "masto-rss-golden-"));
const MAIN = join(ROOT, "src/main.ts");

/** Loggen utan tidsstämplar och med porten utbytt, så att den går att jämföra. */
function clean(out: string): string {
  return out
    .split("\n")
    .map((line) => line.replace(/^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d /, ""))
    .join("\n")
    .replaceAll(String(port), "PORT")
    .replaceAll(root, "DATA");
}

interface Run {
  status: number | null;
  out: string;
  err: string;
}

function run(env: Record<string, string>, args: string[] = [], until?: (out: string) => boolean, onReady?: () => Promise<void>): Promise<Run> {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, [MAIN, ...args], { env: { PATH: process.env.PATH, TZ: "Europe/Stockholm", ...env } });
    let out = "";
    let err = "";
    let fired = false;
    child.stdout.on("data", async (d) => {
      out += d;
      if (until && !fired && until(out)) {
        fired = true;
        await onReady?.();
        child.kill("SIGTERM");
      }
    });
    child.stderr.on("data", (d) => (err += d));
    child.on("close", (status) => resolve({ status, out: clean(out), err: clean(err) }));
  });
}

const statusPort = 20000 + Math.floor(Math.random() * 20000);
const liveEnv = {
  DATA_DIR: join(root, "live"),
  MASTODON_URL: base,
  SIGNAL_API_URL: base,
  SIGNAL_NUMBER: "+46700000000",
  STATUS_PORT: String(statusPort),
  FEED_ALPHA_URL: `${base}/feed-a`,
  FEED_ALPHA_TOKEN: "bra",
  FEED_ALPHA_BACKFILL: "2",
  FEED_BETA_URL: `${base}/broken`,
  FEED_BETA_TOKEN: "bra",
  FEED_BETA_LANGUAGE: "en",
  FAIL_NOTIFY_AFTER: "1",
  FEED_GAMMA_URL: `${base}/feed-c`,
  FEED_GAMMA_TOKEN: "las",
  FEED_DELTA_URL: `${base}/old`,
  FEED_DELTA_TOKEN: "bra3",
  FEED_DELTA_TEMPLATE: "{feed}: {title} ({author})\\n{summary}\\n{link}",
  FEED_PAUSED_ENABLED: "false",
};
let statusJson: unknown;
let statusRaw = "";
const live = await run(liveEnv, [], (out) => /delta: (inget nytt|nytt flöde)/.test(out), async () => {
  const res = await fetch(`http://127.0.0.1:${statusPort}/status`);
  // Porten och tiden för senaste inlägget varierar mellan körningar.
  statusRaw = clean(await res.text()).replace(/"lastPost":"[^"]+"/g, '"lastPost":"TID"');
  statusJson = JSON.parse(statusRaw);
});
const runs: Record<string, unknown> = { live: { ...live, status: live.status } };

// Ett andra varv med samma databas: inget nytt, och flödet som flyttat tipsas om igen.
runs.again = await run({ ...liveEnv, ONCE: "1", STATUS_PORT: "0" });
runs.dryNew = await run({ ...liveEnv, DATA_DIR: join(root, "dry"), DRY_RUN: "1", ONCE: "1", STATUS_PORT: "0", LOG_LEVEL: "debug" });
const importFile = join(root, "processed_entries.txt");
writeFileSync(importFile, "\ufeffhttps://example.com/feed-a/post-1/?utm_source=rss\r\nhttps://www.example.com/feed-a/post-2/\n\n  \n");
runs.import = await run({ ...liveEnv, DATA_DIR: join(root, "imp"), ONCE: "1", STATUS_PORT: "0" }, ["import", "ALPHA", importFile]);
runs.afterImport = await run({ ...liveEnv, DATA_DIR: join(root, "imp"), DRY_RUN: "1", ONCE: "1", STATUS_PORT: "0" });
runs.postDry = await run({ ...liveEnv, DATA_DIR: join(root, "post"), DRY_RUN: "1", STATUS_PORT: "0" }, ["post", "alla", "Hej", "allihop!\\nRad två"]);
runs.post = await run({ ...liveEnv, DATA_DIR: join(root, "post"), STATUS_PORT: "0" }, ["post", "--language", "sv", "alpha,delta", "Hej\\n\\nallihop!"]);
runs.postUsage = await run({ ...liveEnv, DATA_DIR: join(root, "post"), STATUS_PORT: "0" }, ["post", "saknas", "text"]);
runs.importUsage = await run({ ...liveEnv, DATA_DIR: join(root, "post"), STATUS_PORT: "0" }, ["import", "alpha"]);
runs.unknown = await run({ ...liveEnv, DATA_DIR: join(root, "post"), STATUS_PORT: "0" }, ["okänt"]);
runs.noConfig = await run({ DATA_DIR: join(root, "none") });
runs.badConfig = await run({ DATA_DIR: join(root, "none"), FEED_X_URL: "nej", FEED_X_TOKEN: "t", MASTODON_URL: base, STATUS_PORT: "x" });
runs.allPaused = await run({ DATA_DIR: join(root, "paused"), FEED_X_ENABLED: "no", ONCE: "1" });

save("runs.json", {
  runs,
  status: statusJson,
  statusRaw,
  posts,
  signals: [...signals].sort(),
});

server.close();
rmSync(root, { recursive: true, force: true });
