import { test } from "node:test";
import assert from "node:assert/strict";
import { appIcon, layerColor, NEUTRAL, type IconManifest } from "./icons";

const m: IconManifest = {
  youtube: { kind: "svg", color: "#FF0000" },
  slack: { kind: "fav", color: "" },
  bbc: { kind: "fav", color: "#000000" },
  ads: { kind: "letter", color: "#5A6B7D" },
  snapchat: { kind: "svg", color: "#FFFC00" },
  tiktok: { kind: "svg", color: "#000000" },
  netflix: { kind: "svg", color: "#E50914" },
  broken: { kind: "svg", color: "red; background:url(x)" },
  unknown: { kind: "svg", color: "#123456" },
  other: { kind: "svg", color: "#123456" },
};

test("an app with a bundled SVG is drawn from the console's own /icons/<id>.svg on its brand colour", () => {
  assert.deepEqual(appIcon("youtube", m, "YouTube"), { kind: "svg", src: "/icons/youtube.svg", color: "#FF0000", letter: "Y", dark: false });
});

test("an app with a fetched favicon loads /icons/<id> with no extension, on a neutral tile when it has no colour", () => {
  assert.deepEqual(appIcon("slack", m, "Slack"), { kind: "img", src: "/icons/slack", color: NEUTRAL, letter: "S", dark: false });
  assert.deepEqual(appIcon("bbc", m, "BBC"), { kind: "img", src: "/icons/bbc", color: "#000000", letter: "B", dark: false });
});

test("an app the manifest marks letter gets a letter tile and no image at all", () => {
  const i = appIcon("ads", m, "Ads");
  assert.equal(i.kind, "letter");
  assert.equal(i.src, undefined);
  assert.equal(i.color, "#5A6B7D");
  assert.equal(i.letter, "A");
});

test("unknown and other are letter tiles on the neutral colour, whatever the manifest says", () => {
  assert.deepEqual(appIcon("unknown", m, "Unknown · Fastly"), { kind: "letter", color: NEUTRAL, letter: "?", dark: false });
  assert.deepEqual(appIcon("other", m, "other apps"), { kind: "letter", color: NEUTRAL, letter: "…", dark: false });
});

test("an id the manifest does not name is a letter tile, never a guessed URL", () => {
  assert.deepEqual(appIcon("newapp", m, "newapp"), { kind: "letter", color: NEUTRAL, letter: "N", dark: false });
});

test("with no manifest (no icon pack built, or it failed to load) every app is a letter tile", () => {
  assert.deepEqual(appIcon("youtube", null, "YouTube"), { kind: "letter", color: NEUTRAL, letter: "Y", dark: false });
  assert.deepEqual(appIcon("youtube", undefined, "YouTube"), { kind: "letter", color: NEUTRAL, letter: "Y", dark: false });
});

test("an id that is not a plain name never reaches a URL", () => {
  const hostile: IconManifest = { "../api/x": { kind: "svg", color: "#FF0000" }, "A B": { kind: "fav", color: "" } };
  assert.equal(appIcon("../api/x", hostile, "x").kind, "letter");
  assert.equal(appIcon("../api/x", hostile, "x").src, undefined);
  assert.equal(appIcon("A B", hostile, "ab").src, undefined);
});

test("a dotted id keeps its icon, but no id with '..' ever reaches a URL", () => {
  const dotted: IconManifest = { "co.op": { kind: "svg", color: "#00B1E7" }, "news.site": { kind: "fav", color: "" }, "a..b": { kind: "svg", color: "#FF0000" }, "..": { kind: "fav", color: "" } };
  assert.equal(appIcon("co.op", dotted, "Co-op").src, "/icons/co.op.svg");
  assert.equal(appIcon("news.site", dotted, "News").src, "/icons/news.site");
  assert.equal(appIcon("a..b", dotted, "x").src, undefined);
  assert.equal(appIcon("..", dotted, "x").src, undefined);
  assert.equal(appIcon(".hidden", { ".hidden": { kind: "svg", color: "" } }, "x").src, undefined);
});

test("a colour that is not #RRGGBB is replaced by the neutral one", () => {
  assert.equal(appIcon("broken", m, "Broken").color, NEUTRAL);
});

test("a light brand colour gets a dark glyph, a dark one a white glyph", () => {
  assert.equal(appIcon("snapchat", m, "Snapchat").dark, true);
  assert.equal(appIcon("tiktok", m, "TikTok").dark, false);
  assert.equal(appIcon("youtube", m, "YouTube").dark, false);
});

test("the letter is the name's first letter in capitals, or the id's when there is no name", () => {
  assert.equal(appIcon("newapp", m, "").letter, "N");
  assert.equal(appIcon("x1", m, "  zoom").letter, "Z");
});

test("a chart layer takes the brand colour, unless it would vanish on the dark ground or repeat one", () => {
  const used = new Set<string>();
  assert.equal(layerColor("youtube", m, used, "#4C9AFF"), "#FF0000");
  assert.equal(layerColor("tiktok", m, used, "#9BD0FF"), "#9BD0FF"); // black on the dark ground
  assert.equal(layerColor("youtube", m, used, "#7FB2F0"), "#7FB2F0"); // already taken
  assert.equal(layerColor("slack", m, used, "#2F6FD6"), "#2F6FD6"); // no colour
  assert.equal(layerColor("netflix", null, used, "#2F6FD6"), "#2F6FD6"); // no manifest
  assert.deepEqual([...used], ["#FF0000", "#9BD0FF", "#7FB2F0", "#2F6FD6"]);
});
