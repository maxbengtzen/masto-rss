import assert from "node:assert/strict";
import { test } from "node:test";
import { MAX_BYTES, decodeBody, decodeEntities, fetchFeed, htmlToText, HttpError, normalizeLink, parse } from "../src/feed.ts";
import { mockFetch, rss } from "./helpers.ts";

const http = mockFetch();

test("normalizeLink tar bort det som inte gör två länkar olika", () => {
  const plain = "example.com/nyheter/en-post";
  assert.equal(normalizeLink("https://www.example.com/nyheter/en-post/?utm_source=rss&utm_medium=feed"), plain);
  assert.equal(normalizeLink("http://example.com/nyheter/en-post#kommentarer"), plain);
  assert.equal(normalizeLink("  https://EXAMPLE.com/nyheter/en-post  "), plain);
  assert.equal(normalizeLink("https://example.com/?p=12&fbclid=abc&a=1"), "example.com?a=1&p=12");
  assert.equal(normalizeLink("https://example.com:8443/x/"), "example.com:8443/x");
  assert.equal(normalizeLink("tag:example.com,2026:1"), "tag:example.com,2026:1");
  assert.equal(normalizeLink("inte en adress"), "inte en adress");
});

test("decodeEntities och htmlToText ger ren text", () => {
  assert.equal(decodeEntities("Hej &amp; v&#228;lkommen &#x2013; &aring;r&nbsp;2026 &okand;"), "Hej & välkommen – år 2026 &okand;");
  assert.equal(
    htmlToText("<p>Första <b>stycket</b>&nbsp;här.</p><p>Andra<br/>raden &lt;b&gt;</p><script>alert(1)</script><!-- x -->"),
    "Första stycket här. Andra raden <b>",
  );
  assert.equal(htmlToText("  \n "), "");
});

test("decodeBody läser teckenkodningen ur BOM, Content-Type och XML-deklarationen", () => {
  const latin1 = (s: string) => Uint8Array.from([...s].map((c) => c.charCodeAt(0)));
  const xml = '<?xml version="1.0" encoding="ISO-8859-1"?><rss>\xe5\xe4\xf6</rss>';
  assert.equal(decodeBody(latin1(xml), "application/rss+xml"), '<?xml version="1.0" encoding="ISO-8859-1"?><rss>åäö</rss>');
  assert.equal(decodeBody(latin1("<rss>\xe5</rss>"), "text/xml; charset=windows-1252"), "<rss>å</rss>");
  const bom = new Uint8Array([0xef, 0xbb, 0xbf, ...new TextEncoder().encode("<rss>å</rss>")]);
  assert.equal(decodeBody(bom, "text/xml; charset=iso-8859-1"), "<rss>å</rss>");
  assert.equal(decodeBody(new TextEncoder().encode("<rss>å</rss>"), "text/xml; charset=nonsens"), "<rss>å</rss>");
});

test("parse: RSS med CDATA, content:encoded, dc:creator och relativa länkar", () => {
  const feed = parse(
    `<?xml version="1.0"?><rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/">
     <channel><title>Sajten &amp; co</title><language>sv-SE</language>
     <item><title>Titel &#8211; med streck</title><link>/nyheter/1/?utm_source=rss</link>
       <guid isPermaLink="false">https://example.com/?p=1</guid><pubDate>Mon, 26 Oct 2026 08:00:00 +0100</pubDate>
       <description><![CDATA[<p>Kort &amp; gott</p>]]></description><dc:creator><![CDATA[Anna]]></dc:creator></item>
     <item><title>Bara innehåll</title><link>https://example.com/2</link><content:encoded><![CDATA[<p>Hela texten</p>]]></content:encoded><pubDate>trasigt datum</pubDate></item>
     <item><title>Varken id eller länk</title></item>
     </channel></rss>`,
    "https://example.com/feed/",
  );
  assert.equal(feed.title, "Sajten & co");
  assert.equal(feed.language, "sv");
  assert.equal(feed.skipped, 1);
  assert.deepEqual(feed.items[0], {
    keys: ["g:https://example.com/?p=1", "l:example.com/nyheter/1"],
    title: "Titel – med streck",
    link: "https://example.com/nyheter/1/?utm_source=rss",
    summary: "Kort & gott",
    author: "Anna",
    published: Date.UTC(2026, 9, 26, 7),
  });
  assert.deepEqual(feed.items[1].keys, ["l:example.com/2"]);
  assert.equal(feed.items[1].summary, "Hela texten");
  assert.equal(feed.items[1].published, undefined);
});

test("parse: Atom väljer alternate-länken och avkodar HTML-titlar", () => {
  const feed = parse(
    `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom" xml:lang="en-GB">
     <title>Atom</title><id>urn:feed</id><updated>2026-10-26T08:00:00Z</updated>
     <entry><title type="html">Hej &amp;amp; v&#228;lkommen</title><id>tag:example.com,2026:1</id>
       <link rel="enclosure" href="https://example.com/a.mp3"/><link rel="alternate" href="https://example.com/a"/>
       <updated>2026-10-26T07:30:00Z</updated><summary type="html">&lt;p&gt;Sammanfattning&lt;/p&gt;</summary>
       <author><name>Bo</name></author></entry>
     </feed>`,
    "https://example.com/atom.xml",
  );
  assert.equal(feed.language, "en");
  assert.deepEqual(feed.items[0], {
    keys: ["g:tag:example.com,2026:1", "l:example.com/a"],
    title: "Hej & välkommen",
    link: "https://example.com/a",
    summary: "Sammanfattning",
    author: "Bo",
    published: Date.UTC(2026, 9, 26, 7, 30),
  });
});

test("parse: RDF och JSON Feed", () => {
  const rdf = parse(
    `<?xml version="1.0"?><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
     <channel rdf:about="https://example.com/"><title>RDF</title><link>https://example.com/</link><dc:language>sv</dc:language></channel>
     <item rdf:about="https://example.com/r1"><title>RDF-post</title><link>https://example.com/r1</link><dc:date>2026-10-26T06:00:00Z</dc:date></item>
     </rdf:RDF>`,
    "https://example.com/rdf",
  );
  assert.equal(rdf.language, "sv");
  assert.deepEqual(rdf.items[0].keys, ["g:https://example.com/r1", "l:example.com/r1"]);
  assert.equal(rdf.items[0].published, Date.UTC(2026, 9, 26, 6));

  const json = parse(
    JSON.stringify({
      version: "https://jsonfeed.org/version/1.1",
      title: "JSON",
      language: "sv",
      items: [{ id: "j1", url: "https://example.com/j1", title: "JSON-post", content_html: "<p>Text</p>", date_published: "2026-10-26T05:00:00Z", authors: [{ name: "Cia" }] }],
    }),
    "https://example.com/feed.json",
  );
  assert.deepEqual(json.items[0], {
    keys: ["g:j1", "l:example.com/j1"],
    title: "JSON-post",
    link: "https://example.com/j1",
    summary: "Text",
    author: "Cia",
    published: Date.UTC(2026, 9, 26, 5),
  });
});

test("parse: trasigt innehåll ger ett fel", () => {
  assert.throws(() => parse("<html><body>Inte ett flöde</body></html>", "https://example.com/"));
});

test("fetchFeed skickar ETag och Last-Modified och förstår 304", async () => {
  http.set(() => new Response(null, { status: 304 }));
  const result = await fetchFeed("https://example.com/feed", { etag: '"abc"', lastModified: "Mon, 26 Oct 2026 08:00:00 GMT" });
  assert.deepEqual(result, { status: "not-modified", redirect: undefined });
  const headers = http.requests[0].headers;
  assert.equal(headers.get("if-none-match"), '"abc"');
  assert.equal(headers.get("if-modified-since"), "Mon, 26 Oct 2026 08:00:00 GMT");
  assert.match(headers.get("user-agent") ?? "", /^masto-rss\//);
});

test("fetchFeed returnerar texten och nya ETag/Last-Modified", async () => {
  http.set(() => new Response(rss([]), { headers: { etag: '"v2"', "last-modified": "Tue, 27 Oct 2026 08:00:00 GMT" } }));
  const result = await fetchFeed("https://example.com/feed");
  assert.equal(result.status, "ok");
  assert.ok(result.status === "ok" && result.text.includes("<rss"));
  assert.ok(result.status === "ok" && result.etag === '"v2"' && result.lastModified === "Tue, 27 Oct 2026 08:00:00 GMT");
  assert.equal(http.requests[0].headers.get("if-none-match"), null);
});

test("fetchFeed följer omdirigeringar och säger om de var permanenta", async () => {
  const chain: Record<string, Response> = {};
  http.set((url) => chain[url] ?? new Response(rss([])));

  chain["https://www.example.com/rss"] = new Response(null, { status: 308, headers: { location: "/rss/" } });
  let result = await fetchFeed("https://www.example.com/rss");
  assert.deepEqual(result.redirect, { to: "https://www.example.com/rss/", permanent: true });

  chain["https://a.example/feed"] = new Response(null, { status: 301, headers: { location: "https://b.example/feed" } });
  chain["https://b.example/feed"] = new Response(null, { status: 302, headers: { location: "https://c.example/feed" } });
  result = await fetchFeed("https://a.example/feed");
  assert.deepEqual(result.redirect, { to: "https://c.example/feed", permanent: false });
});

test("fetchFeed ger upp efter för många omdirigeringar", async () => {
  http.set((url) => new Response(null, { status: 302, headers: { location: `${url}x` } }));
  await assert.rejects(fetchFeed("https://example.com/loop"), /Fler än 5 omdirigeringar/);
});

test("fetchFeed ger HttpError med Retry-After", async () => {
  http.set(() => new Response("upptagen", { status: 503, statusText: "Service Unavailable", headers: { "retry-after": "120" } }));
  await assert.rejects(fetchFeed("https://example.com/feed"), (err: unknown) => {
    assert.ok(err instanceof HttpError);
    assert.equal(err.status, 503);
    assert.equal(err.retryAfter, 120);
    assert.equal(err.message, "HTTP 503 Service Unavailable");
    return true;
  });
});

test("fetchFeed stoppar för stora svar", async () => {
  http.set(() => new Response("x", { headers: { "content-length": String(MAX_BYTES + 1) } }));
  await assert.rejects(fetchFeed("https://example.com/feed"), /större än 5 MB/);

  const chunk = new Uint8Array(1024 * 1024);
  let sent = 0;
  http.set(() => new Response(new ReadableStream({ pull(c) { sent++; c.enqueue(chunk); } })));
  await assert.rejects(fetchFeed("https://example.com/feed"), /större än 5 MB/);
  assert.ok(sent < 10, `läste ${sent} MB innan den slutade`);
});
