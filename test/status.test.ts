import assert from "node:assert/strict";
import { test } from "node:test";
import { ago, daysAgo, startStatusServer } from "../src/status.ts";

const NOW = Date.UTC(2026, 9, 26, 12);

test("ago och daysAgo skriver tiden på svenska", () => {
  assert.equal(ago(NOW - 20_000, NOW), "nyss");
  assert.equal(ago(NOW - 12 * 60_000, NOW), "12 min sedan");
  assert.equal(ago(NOW - 3 * 3600_000, NOW), "3 h sedan");
  assert.equal(ago(NOW - 26 * 3600_000, NOW), "1 dag sedan");
  assert.equal(ago(NOW - 80 * 3600_000, NOW), "3 dagar sedan");
  assert.equal(daysAgo("2026-10-26", NOW, "Europe/Stockholm"), "idag");
  assert.equal(daysAgo("2026-10-25", NOW, "Europe/Stockholm"), "igår");
  assert.equal(daysAgo("2026-05-12", NOW, "Europe/Stockholm"), "167 dagar sedan");
});

test("statussidan svarar med JSON på /status och 404 annars", async () => {
  const snapshot = { version: "1.0.0", totals: { feeds: 1, postedLastDay: 2, errors: 0 }, feeds: [] };
  const server = startStatusServer(0, () => snapshot);
  await new Promise((resolve) => server.once("listening", resolve));
  const { port } = server.address() as { port: number };
  try {
    const res = await fetch(`http://127.0.0.1:${port}/status`);
    assert.equal(res.headers.get("content-type"), "application/json; charset=utf-8");
    assert.deepEqual(await res.json(), snapshot);
    assert.equal((await fetch(`http://127.0.0.1:${port}/annat`)).status, 404);
  } finally {
    server.close();
  }
});
