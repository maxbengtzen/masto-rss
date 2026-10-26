/** Loggrader med tid i containerns tidszon (TZ). */

let debugEnabled = false;

export function setDebug(enabled: boolean): void {
  debugEnabled = enabled;
}

export function log(msg: string): void {
  const ts = new Date().toLocaleString("sv-SE", { timeZone: process.env.TZ || undefined });
  console.log(`${ts} ${msg}`);
}

export function debug(msg: string): void {
  if (debugEnabled) log(msg);
}

/** Kort beskrivning av ett fel för loggen, med orsaken om det finns en. */
export function describe(err: unknown): string {
  if (err instanceof Error) {
    const cause = err.cause instanceof Error ? ` (${err.cause.message})` : "";
    return `${err.name}: ${err.message}${cause}`;
  }
  return String(err);
}
