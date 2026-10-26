/**
 * Statussidan för en samlad Homepage-ruta: GET /status ger JSON med varje flödes konto, följare,
 * senaste inlägg och eventuella fel. Den är tänkt för ett internt nätverk och visar inga tokens.
 */
import { createServer, type Server } from "node:http";

export interface FeedStatus {
  name: string;
  /** Till exempel "@feber". */
  account: string;
  /** Kontots profilsida. */
  url?: string;
  followers?: number;
  posts?: number;
  /** Senaste inlägget (ISO 8601), om det är känt. */
  lastPost?: string;
  ok: boolean;
  /** En rad för Homepage, till exempel "195 följare · 12 min sedan". */
  summary: string;
}

export interface StatusSnapshot {
  version: string;
  /** postedLastDay: inlägg de senaste 24 timmarna. */
  totals: { feeds: number; postedLastDay: number; errors: number };
  feeds: FeedStatus[];
}

export function startStatusServer(port: number, snapshot: () => StatusSnapshot): Server {
  const server = createServer((req, res) => {
    if (req.method !== "GET" || (req.url !== "/status" && req.url !== "/")) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
    res.end(JSON.stringify(snapshot()));
  });
  server.listen(port);
  return server;
}

/** Hur länge sedan, på svenska: "nyss", "12 min sedan", "3 h sedan", "2 dagar sedan". */
export function ago(then: number, now: number): string {
  const minutes = Math.floor((now - then) / 60_000);
  if (minutes < 1) return "nyss";
  if (minutes < 60) return `${minutes} min sedan`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} h sedan`;
  const days = Math.floor(hours / 24);
  return days === 1 ? "1 dag sedan" : `${days} dagar sedan`;
}

/** Samma sak för ett datum utan klockslag (YYYY-MM-DD), räknat i `timeZone`. */
export function daysAgo(date: string, now: number, timeZone?: string): string {
  const today = new Date(now).toLocaleDateString("sv-SE", { timeZone });
  const days = Math.round((Date.parse(today) - Date.parse(date)) / 86_400_000);
  if (days <= 0) return "idag";
  return days === 1 ? "igår" : `${days} dagar sedan`;
}
