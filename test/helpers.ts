/** Hjälp för testerna: låtsad fetch och små påhittade flöden. Inget nätverk. */

export interface Request {
  url: string;
  headers: Headers;
}

type Handler = (url: string) => Response | Promise<Response>;

/** Ersätter globalThis.fetch. `handler` svarar på varje anrop; alla anrop sparas i `requests`. */
export function mockFetch(): { requests: Request[]; set(handler: Handler): void } {
  const requests: Request[] = [];
  let handler: Handler = (url) => {
    throw new Error(`oväntat anrop till ${url}`);
  };
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input instanceof Object && "url" in input ? input.url : input);
    requests.push({ url, headers: new Headers(init?.headers) });
    return handler(url);
  }) as typeof fetch;
  return {
    requests,
    set(h: Handler) {
      handler = h;
      requests.length = 0;
    },
  };
}

export interface TestItem {
  guid?: string;
  link?: string;
  title?: string;
  /** RFC 822, som i RSS. */
  date?: string;
  description?: string;
}

export function rss(items: TestItem[], language = "sv-SE"): string {
  const body = items
    .map((i) =>
      [
        "<item>",
        i.title !== undefined ? `<title>${i.title}</title>` : "",
        i.link ? `<link>${i.link}</link>` : "",
        i.guid ? `<guid isPermaLink="false">${i.guid}</guid>` : "",
        i.date ? `<pubDate>${i.date}</pubDate>` : "",
        i.description ? `<description><![CDATA[${i.description}]]></description>` : "",
        "</item>",
      ].join(""),
    )
    .join("\n");
  return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/">
<channel><title>Testflöde</title><link>https://example.com/</link><language>${language}</language>
${body}
</channel></rss>`;
}

/** Poster nummer `from`..`to`, nyast först som i ett vanligt flöde, en timme mellan varje. */
export function numbered(from: number, to: number, newest = Date.UTC(2026, 9, 26, 12)): TestItem[] {
  const items: TestItem[] = [];
  for (let n = to; n >= from; n--) {
    items.push({
      guid: `https://example.com/?p=${n}`,
      link: `https://example.com/post-${n}/?utm_source=rss`,
      title: `Post ${n}`,
      date: new Date(newest - (to - n) * 3600_000).toUTCString(),
    });
  }
  return items;
}
