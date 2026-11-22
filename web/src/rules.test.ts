import { test } from "node:test";
import assert from "node:assert/strict";
import {
  describeRule, ruleFromForm, nextLogin, daysText, stateOf, pauseBody, pauseUntil, blockedTile, appliesTo, manifestName, appChoices, isPause, formProblem, formFromRule,
  liveAllows, allowText, activePause, loginWarning, usageText, usageLabel,
  type Rule, type RuleForm,
} from "./rules";

const rule = (o: Partial<Rule> = {}): Rule => ({
  id: "r_1", enabled: true, what: "app", targets: ["tiktok", "youtube"], who: "device", members: ["aa:bb:cc:dd:ee:01"], group: "",
  days: ["mon", "tue", "wed", "thu", "fri"], from: "21:00", to: "07:00", until: null, activeNow: false, hits: 0, applied: true, problem: "", error: "", allows: [],
  ...o,
});
const names = {
  app: (id: string) => ({ tiktok: "TikTok", youtube: "YouTube" })[id as "tiktok"] ?? id,
  device: (m: string) => (m === "aa:bb:cc:dd:ee:01" ? "Alex's iPhone" : m),
  group: (g: string) => (g === "kids" ? "Kids" : g),
};

test("a rule reads as what, who and when", () => {
  assert.equal(describeRule(rule(), names), "TikTok, YouTube · Alex's iPhone · Mon-Fri 21:00-07:00");
  assert.equal(describeRule(rule({ what: "country", targets: ["RU", "CN"], who: "all", members: [], days: [], from: "", to: "" })), "Russia, China · all devices · always");
  assert.equal(describeRule(rule({ what: "category", targets: ["video"], who: "group", group: "kids", members: [], days: ["sat", "sun"], from: "", to: "" }), names), "Video · group \"Kids\" · Sat, Sun");
  // A pause: everything, one device, until a moment (shown in London).
  assert.equal(
    describeRule(rule({ what: "all", targets: [], days: [], from: "", to: "", until: Date.parse("2026-11-21T07:00:00Z") / 1000 }), names),
    "the whole internet · Alex's iPhone · until 21 Nov 07:00",
  );
});

test("days are written as ranges only when there are three in a row", () => {
  assert.equal(daysText(["fri", "mon", "tue", "wed", "thu", "sat", "sun"]), "every day");
  assert.equal(daysText(["mon", "tue"]), "Mon, Tue");
  assert.equal(daysText(["mon", "wed", "thu", "fri", "sun"]), "Mon, Wed-Fri, Sun");
  assert.equal(daysText([]), "");
});

const form = (o: Partial<RuleForm> = {}): RuleForm => ({ what: "app", targets: "", who: "all", members: [], group: "", days: [], from: "", to: "", ...o });

test("a form becomes the API's rule body", () => {
  assert.deepEqual(ruleFromForm(form({ what: "country", targets: "ru, cn;de", who: "all" })), { what: "country", who: "all", targets: ["RU", "CN", "DE"] });
  assert.deepEqual(
    ruleFromForm(form({ targets: "TikTok  YouTube", who: "device", members: ["AA:BB:CC:DD:EE:01"], days: ["fri", "mon"], from: "21:00", to: "07:00" })),
    { what: "app", who: "device", targets: ["tiktok", "youtube"], members: ["aa:bb:cc:dd:ee:01"], days: ["mon", "fri"], from: "21:00", to: "07:00" },
  );
  assert.deepEqual(ruleFromForm(form({ what: "category", targets: "video", who: "group", group: "kids" })), { what: "category", who: "group", targets: ["video"], group: "kids" });
  // "All" has no targets; a time needs both ends; a device rule sends no group.
  assert.deepEqual(ruleFromForm(form({ what: "all", targets: "ignored", who: "device", members: ["aa:bb:cc:dd:ee:01"], from: "21:00" })), { what: "all", who: "device", members: ["aa:bb:cc:dd:ee:01"] });
  assert.deepEqual(ruleFromForm(form({ what: "all", enabled: false })), { what: "all", who: "all", enabled: false });
});

test("a form says what is wrong before it is sent", () => {
  assert.equal(formProblem(form({ what: "country", targets: "" })), "Name the countries, for example RU, CN.");
  assert.equal(formProblem(form({ what: "country", targets: "Russia" })), "Countries are written as two letters: RU, CN.");
  assert.equal(formProblem(form({ targets: "tiktok", who: "device" })), "Pick a device.");
  assert.equal(formProblem(form({ targets: "tiktok", who: "group" })), "Pick a group.");
  assert.equal(formProblem(form({ targets: "tiktok", from: "21:00" })), "A time needs both a start and an end.");
  assert.equal(formProblem(form({ targets: "tiktok" })), "");
  assert.equal(formProblem(form({ what: "all" })), "");
});

test("editing a rule gives back the form that wrote it", () => {
  const r = rule();
  assert.deepEqual(ruleFromForm(formFromRule(r)), {
    what: "app", who: "device", enabled: true, targets: ["tiktok", "youtube"], members: ["aa:bb:cc:dd:ee:01"],
    days: ["mon", "tue", "wed", "thu", "fri"], from: "21:00", to: "07:00",
  });
});

test("an edit keeps a pause's end", () => {
  const r = rule({ what: "all", targets: [], days: [], from: "", to: "", until: 1795248000 });
  assert.deepEqual(ruleFromForm({ ...formFromRule(r), enabled: false }), { what: "all", who: "device", enabled: false, members: ["aa:bb:cc:dd:ee:01"], until: 1795248000 });
});

test("after a 401 the login returns to the screen it interrupted", () => {
  assert.equal(nextLogin("#/client/aa:bb:cc:dd:ee:01"), "#/client/aa:bb:cc:dd:ee:01");
  assert.equal(nextLogin("#/flows?q=mac%3Aaa"), "#/flows?q=mac%3Aaa");
  assert.equal(nextLogin("#/rules"), "#/rules");
  assert.equal(nextLogin(""), "#/");
  assert.equal(nextLogin("#"), "#/");
  // Only a place on this page: no address, no script, no control character, nothing huge.
  assert.equal(nextLogin("ftp://router.example/"), "#/");
  assert.equal(nextLogin("javascript:alert(1)"), "#/");
  assert.equal(nextLogin("#/rules\nx"), "#/");
  assert.equal(nextLogin("#/" + "a".repeat(600)), "#/");
});

test("a rule's state says why it is not working", () => {
  const now = Date.parse("2026-11-21T12:00:00Z");
  assert.deepEqual(stateOf(rule({ activeNow: true }), now), { kind: "active", text: "active now" });
  assert.deepEqual(stateOf(rule(), now), { kind: "waiting", text: "waiting for its time" });
  assert.deepEqual(stateOf(rule({ error: "unknown app x" }), now), { kind: "off", text: "off: unknown app x" });
  assert.deepEqual(stateOf(rule({ enabled: false, activeNow: true }), now), { kind: "off", text: "off: turned off" });
  assert.deepEqual(stateOf(rule({ until: now / 1000 - 5 }), now), { kind: "off", text: "off: its time has ended" });
  assert.deepEqual(stateOf(rule({ problem: "not applied: nft refused" }), now), { kind: "failed", text: "not applied: nft refused" });
  assert.deepEqual(stateOf(rule({ problem: "nft refused" }), now), { kind: "failed", text: "not applied: nft refused" });
});

test("a pause ends in an hour, at the next 07:00 of the router's zone, or never", () => {
  const t = (iso: string) => new Date(iso);
  const sec = (iso: string) => Date.parse(iso) / 1000;
  // Nairobi is UTC+3 all year, and the test machine is not in it.
  assert.equal(pauseUntil("07:00", t("2026-11-21T20:00:00Z"), "Africa/Nairobi"), sec("2026-11-22T04:00:00Z"));
  assert.equal(pauseUntil("07:00", t("2026-11-21T01:00:00Z"), "Africa/Nairobi"), sec("2026-11-21T04:00:00Z"));
  assert.equal(pauseUntil("07:00", t("2026-11-21T04:00:00Z"), "Africa/Nairobi"), sec("2026-11-22T04:00:00Z"));
  // Across the clocks going forward in London (29 March 2026): 07:00 is BST there.
  assert.equal(pauseUntil("07:00", t("2026-03-28T22:00:00Z"), "Europe/London"), sec("2026-03-29T06:00:00Z"));
  assert.equal(pauseUntil("07:00", t("2026-11-21T22:00:00Z"), "UTC"), sec("2026-11-22T07:00:00Z"));
  assert.equal(pauseUntil("1h", t("2026-11-21T20:00:00Z"), "Africa/Nairobi"), sec("2026-11-21T21:00:00Z"));
  assert.equal(pauseUntil("forever", t("2026-11-21T20:00:00Z"), "UTC"), undefined);
  const now = t("2026-11-21T20:00:00Z");
  assert.deepEqual(pauseBody("AA:BB:CC:DD:EE:01", "forever", now, "UTC"), { what: "all", who: "device", members: ["aa:bb:cc:dd:ee:01"] });
  assert.equal(pauseBody("aa:bb:cc:dd:ee:01", "1h", now, "UTC").until, sec("2026-11-21T21:00:00Z"));
  assert.equal(isPause(rule({ what: "all", targets: [], days: [], from: "", to: "" })), true);
  assert.equal(isPause(rule()), false);
});

test("a rule reaches a device by everyone, its MAC or its group in UCI", () => {
  assert.equal(appliesTo(rule({ who: "all" }), "aa:bb:cc:dd:ee:09"), true);
  assert.equal(appliesTo(rule(), "AA:BB:CC:DD:EE:01"), true);
  assert.equal(appliesTo(rule(), "aa:bb:cc:dd:ee:02"), false);
  // A group rule's members are the router's expansion of the UCI group: a
  // group that only labels.json gives the device does not count.
  assert.equal(appliesTo(rule({ who: "group", group: "kids", members: ["aa:bb:cc:dd:ee:02"] }), "AA:BB:CC:DD:EE:02"), true);
  assert.equal(appliesTo(rule({ who: "group", group: "kids", members: [] }), "aa:bb:cc:dd:ee:02"), false);
});

test("a rule's live allowances say until when and whose", () => {
  const now = Date.parse("2026-11-21T12:00:00Z");
  const sec = (iso: string) => Date.parse(iso) / 1000;
  const r = rule({
    allows: [
      { id: "a_1", member: "aa:bb:cc:dd:ee:01", until: sec("2026-11-21T12:30:00Z"), by: "owner" },
      { id: "agent_x", member: "aa:bb:cc:dd:ee:02", until: sec("2026-11-21T13:00:00Z"), by: "agent" },
      { id: "a_old", member: "aa:bb:cc:dd:ee:01", until: sec("2026-11-21T11:00:00Z"), by: "owner" },
    ],
  });
  assert.deepEqual(liveAllows(r, now).map((a) => a.id), ["a_1", "agent_x"]);
  assert.deepEqual(liveAllows(r, now, "AA:BB:CC:DD:EE:02").map((a) => a.id), ["agent_x"]);
  assert.equal(allowText(r.allows![0]), "allowed until 12:30");
  assert.equal(allowText(r.allows![1]), "allowed until 13:00, by the agent");
  // A router that does not send allows yet lists none.
  assert.deepEqual(liveAllows(rule({ allows: undefined }), now), []);
});

test("the device card offers Pause only while no pause is on", () => {
  const now = Date.parse("2026-11-21T12:00:00Z");
  const mac = "aa:bb:cc:dd:ee:01";
  const pause = rule({ id: "r_p", what: "all", targets: [], days: [], from: "", to: "", until: now / 1000 + 600 });
  assert.equal(activePause([rule(), pause], mac, now)?.id, "r_p");
  assert.equal(activePause([rule(), { ...pause, until: now / 1000 - 1 }], mac, now), undefined);
  assert.equal(activePause([{ ...pause, enabled: false }], mac, now), undefined);
  assert.equal(activePause([pause], "aa:bb:cc:dd:ee:02", now), undefined);
  assert.equal(activePause(undefined, mac, now), undefined);
});

test("the login warns when the password would cross the house network in the clear", () => {
  assert.match(loginWarning("http:"), /unencrypted/);
  assert.equal(loginWarning("https:"), "");
});

test("the Blocked tile sums the counters and says they run from the rules' start", () => {
  assert.deepEqual(blockedTile([rule({ hits: 1200 }), rule({ hits: 34 })]), { value: new Intl.NumberFormat("en-GB").format(1234), sub: "since the rules started" });
  assert.deepEqual(blockedTile([]), { value: "-", sub: "no rules" });
  assert.deepEqual(blockedTile(undefined), { value: "-", sub: "no data" });
  assert.deepEqual(blockedTile([rule({ hits: 0 })]), { value: "0", sub: "since the rules started" });
});

test("a rule names an app as the icon manifest does, and by id when the manifest does not know it", () => {
  const name = manifestName({ google: { name: "Google" }, ads: {} });
  assert.equal(name("google"), "Google");
  assert.equal(name("nosuch"), "nosuch");
  assert.equal(name("ads"), "ads");
  assert.equal(manifestName(null)("google"), "google");
  assert.equal(manifestName({})("toString"), "toString");
});

test("the app picker merges the manifest's names with the apps the console has seen", () => {
  assert.deepEqual(appChoices({ google: { name: "Google" }, ads: { name: "Ads" } }, [{ id: "google", name: "google.com" }, { id: "tiktok", name: "TikTok" }, { id: "unknown" }, { id: "x1" }]), [
    { id: "google", name: "Google" }, { id: "tiktok", name: "TikTok" }, { id: "x1", name: "x1" },
  ]);
  // No icon pack: the seen apps still make a list.
  assert.deepEqual(appChoices(null, [{ id: "tiktok", name: "TikTok" }]), [{ id: "tiktok", name: "TikTok" }]);
  assert.deepEqual(appChoices({}, []), []);
});

test("a limit rule reads as minutes a day", () => {
  const lim = rule({ action: "limit", minutes: 60, targets: ["tiktok"], days: [], from: "", to: "" });
  assert.equal(describeRule(lim, names), "TikTok · Alex's iPhone · 60 min a day");
  assert.equal(describeRule({ ...lim, days: ["sat", "sun"] }, names), "TikTok · Alex's iPhone · Sat, Sun · 60 min a day");
  const f: RuleForm = { what: "app", targets: "tiktok", who: "device", members: ["aa:bb:cc:dd:ee:01"], group: "", days: [], from: "", to: "", action: "limit", minutes: "60" };
  assert.deepEqual(ruleFromForm(f), { what: "app", who: "device", targets: ["tiktok"], members: ["aa:bb:cc:dd:ee:01"], action: "limit", minutes: 60 });
  assert.equal(formProblem({ ...f, minutes: "0" }), "Time limit: 1 to 1440 minutes a day.");
  assert.equal(formProblem({ ...f, what: "all" }), "A time limit is only for an app or a category.");
  assert.equal(formProblem({ ...f, from: "21:00", to: "07:00" }), "A time limit counts the whole day: no hours are needed.");
  assert.equal(formFromRule(lim).action, "limit");
  assert.equal(formFromRule(lim).minutes, "60");
});

test("usage reads as used of limit", () => {
  assert.equal(usageText({ used: 12, limit: 60, spentUntil: null }), "used 12 of 60 min");
  const spent = Date.parse("2026-11-22T00:00:00Z") / 1000;
  assert.equal(usageText({ used: 60, limit: 60, spentUntil: spent }, "Europe/London"), "used up until 00:00");
});

test("a limit's usage names the apps that were used, with their minutes", () => {
  const name = (id: string) => ({ tiktok: "TikTok", instagram: "Instagram" } as Record<string, string>)[id] ?? id;
  const u = { rule: "r", what: "category", targets: ["social"], used: 60, limit: 60, spentUntil: null,
    apps: [{ app: "tiktok", used: 40 }, { app: "instagram", used: 20 }] };
  assert.equal(usageLabel(u, name), "Social media (TikTok 40 min, Instagram 20 min)");
  assert.equal(usageLabel({ ...u, apps: [] }, name), "Social media");
  assert.equal(usageLabel({ ...u, what: "app", targets: ["tiktok"], apps: [{ app: "tiktok", used: 40 }] }, name), "TikTok");
});
