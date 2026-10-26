/**
 * Aviseringar via signal-cli-rest-api, skickade till numret självt. Av om SIGNAL_API_URL eller
 * SIGNAL_NUMBER saknas. En avisering får aldrig fälla något, och i torrläge skickas inget.
 */
import { describe, log } from "./log.ts";

export interface Notifier {
  (message: string): Promise<void>;
}

export function createNotifier(url: string, number: string, dryRun: boolean): Notifier {
  return async (message: string) => {
    const text = `masto-rss: ${message}`;
    if (!url || !number) return;
    if (dryRun) {
      log(`[torrläge] skulle avisera: ${text}`);
      return;
    }
    try {
      const res = await fetch(`${url}/v2/send`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ message: text, number, recipients: [number] }),
        signal: AbortSignal.timeout(30_000),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status} ${(await res.text()).slice(0, 200)}`);
    } catch (err) {
      log(`Kunde inte skicka Signal-avisering: ${describe(err)}`);
    }
  };
}
