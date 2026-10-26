/**
 * Inläggets text: mallen fylls i, `@` i text från flödet neutraliseras, och texten kortas tills den
 * ryms så som Mastodon räknar: det man ser som ett tecken (grafemkluster) räknas som ett, och varje
 * länk som ett fast antal tecken. Länkar kortas aldrig.
 */

export interface Limits {
  maxCharacters: number;
  /** Tecken per länk. Saknas värdet räknas länkar i full längd, vilket är det försiktiga valet. */
  charactersPerUrl?: number;
}

export interface StatusFields {
  title: string;
  link?: string;
  summary: string;
  author?: string;
  feed?: string;
}

const URL_RE = /https?:\/\/[^\s<>"]+/g;
const segmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });

function graphemes(text: string): string[] {
  return Array.from(segmenter.segment(text), (s) => s.segment);
}

export function statusLength(text: string, limits: Limits): number {
  let length = 0;
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    length += graphemes(text.slice(last, m.index)).length + (limits.charactersPerUrl ?? graphemes(m[0]).length);
    last = m.index + m[0].length;
  }
  return length + graphemes(text.slice(last)).length;
}

/**
 * Ett osynligt tecken efter `@` gör att Mastodon inte tolkar `@namn` i flödestexten som ett
 * omnämnande. `@` direkt efter ett ordtecken, `/` eller `=` (e-postadresser, länkar) rörs inte, eftersom
 * Mastodon inte heller tolkar dem.
 */
export function defuseMentions(text: string): string {
  return text.replace(/(^|[^\w/=])@(?=\w)/g, "$1@​");
}

/** Högst `max` tecken, med "…" sist om något kortades bort. */
function shorten(text: string, max: number): string {
  const chars = graphemes(text);
  if (chars.length <= max) return text;
  if (max <= 1) return "";
  return `${chars.slice(0, max - 1).join("").trimEnd()}…`;
}

/** Bort med tomma rader i början och slutet, och högst en tom rad i följd. */
function tidy(text: string): string {
  return text
    .split("\n")
    .map((line) => line.trimEnd())
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

type Field = "title" | "summary" | "author" | "feed";

export function renderStatus(template: string, fields: StatusFields, limits: Limits): string {
  const values: Record<Field | "link", string> = {
    title: defuseMentions(fields.title),
    link: fields.link ?? "",
    summary: defuseMentions(fields.summary),
    author: defuseMentions(fields.author ?? ""),
    feed: defuseMentions(fields.feed ?? ""),
  };
  const render = () => tidy(template.replace(/\{(\w+)\}/g, (_, key: string) => values[key as Field] ?? ""));
  const fits = () => statusLength(render(), limits) <= limits.maxCharacters;

  // Korta i den här ordningen tills texten ryms. Varje fält får den största längd som fungerar.
  for (const field of ["summary", "title", "author", "feed"] as const) {
    if (fits()) break;
    const full = values[field];
    let low = 0;
    let high = graphemes(full).length;
    while (low < high) {
      const mid = Math.ceil((low + high) / 2);
      values[field] = shorten(full, mid);
      if (fits()) low = mid;
      else high = mid - 1;
    }
    values[field] = shorten(full, low);
  }
  return render();
}
