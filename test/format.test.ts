import assert from "node:assert/strict";
import { test } from "node:test";
import { defuseMentions, renderStatus, statusLength } from "../src/format.ts";

const mastodon = { maxCharacters: 500, charactersPerUrl: 23 };

test("statusLength räknar som Mastodon: länkar som 23 tecken, emoji som ett", () => {
  assert.equal(statusLength("hej", mastodon), 3);
  assert.equal(statusLength("läs https://example.com/en/mycket/lang/adress?utm_source=rss här", mastodon), 4 + 23 + 4);
  assert.equal(statusLength("👨‍👩‍👧 åäö", mastodon), 5);
  // Utan känd länklängd räknas länken i sin helhet.
  assert.equal(statusLength("https://example.com/a", { maxCharacters: 500 }), 21);
});

test("defuseMentions stoppar omnämnanden men inte e-post och länkar", () => {
  assert.equal(defuseMentions("Tack @anna och @bo@example.social!"), "Tack @​anna och @​bo@example.social!");
  assert.equal(defuseMentions("@först"), "@​först");
  assert.equal(defuseMentions("mejla info@example.com"), "mejla info@example.com");
  assert.equal(defuseMentions("https://medium.com/@skribent/inlagg"), "https://medium.com/@skribent/inlagg");
});

test("renderStatus fyller i mallen och städar tomma rader", () => {
  const fields = { title: "Rubrik", link: "https://example.com/a", summary: "Kort text", author: "Anna", feed: "Sajten" };
  assert.equal(renderStatus("{title}\n\n{link}", fields, mastodon), "Rubrik\n\nhttps://example.com/a");
  assert.equal(renderStatus("{feed}: {title} ({author})\n{summary}\n{link}", fields, mastodon), "Sajten: Rubrik (Anna)\nKort text\nhttps://example.com/a");
  assert.equal(renderStatus("{title}\n\n{summary}\n\n{link}", { ...fields, summary: "" }, mastodon), "Rubrik\n\nhttps://example.com/a");
  assert.equal(renderStatus("{title}\n\n{link}", { title: "", summary: "", link: "https://example.com/a" }, mastodon), "https://example.com/a");
});

test("renderStatus kortar sammanfattningen först, sedan titeln, men aldrig länken", () => {
  const link = "https://example.com/en/lang/adress";
  const small = { maxCharacters: 60, charactersPerUrl: 23 };
  const text = renderStatus("{title}\n{summary}\n{link}", { title: "Rubrik", summary: "x".repeat(100), link }, small);
  assert.ok(text.endsWith(`…\n${link}`), text);
  assert.ok(text.startsWith("Rubrik\nxxx"));
  assert.equal(statusLength(text, small), 60);

  const long = renderStatus("{title}\n{link}", { title: "Ord ".repeat(30), summary: "", link }, small);
  assert.ok(long.endsWith(`…\n${link}`), long);
  assert.ok(statusLength(long, small) <= 60);
});
