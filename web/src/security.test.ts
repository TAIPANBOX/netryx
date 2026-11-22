import { test } from "node:test";
import assert from "node:assert/strict";
import { setZone } from "./zone";
import { alertText, pendingText, telegramText, alertsBody, alertsTile, quarantineText, type Alert, type AlertsState } from "./security";
import { parseRoute } from "./query";

const names: Record<string, string> = { "02:4e:58:00:00:04": "Camera", "02:4e:58:00:00:03": "iPhone" };
const name = (mac: string) => names[mac] ?? "";

test("each kind of alert reads as one line", () => {
  setZone("Europe/London");
  const at = "2026-11-21T12:05:00Z";
  const cases: [Alert, string][] = [
    [{ id: "1", kind: "new_device", mac: "02:4e:58:00:00:04", at }, "12:05 New device on the network: Camera"],
    [{ id: "2", kind: "blocked_country", mac: "02:4e:58:00:00:04", country: "RU", at }, "12:05 Connection attempt to blocked country Russia: Camera"],
    [{ id: "3", kind: "upload", mac: "02:4e:58:00:00:04", bytes: 500e6, at }, "12:05 Unusually large upload: Camera, 477 MB in an hour"],
    [{ id: "4", kind: "quarantine_full", at }, "12:05 Too many new devices: the quarantine list is full"],
    [{ id: "5", kind: "baseline_rebuilt", at }, "12:05 The list of known devices was rebuilt: for 24 hours new devices join it without quarantine"],
    [{ id: "6", kind: "new_device", mac: "02:00:00:00:00:09", at }, "12:05 New device on the network: 02:00:00:00:00:09"],
    [{ id: "7", kind: "quarantine_off", at }, "12:05 Quarantine is off: the list of known devices cannot be read"],
  ];
  for (const [a, want] of cases) assert.equal(alertText(a, name), want);
});

test("a quarantined device with a known hostname names the likely device", () => {
  const d = { mac: "06:11:22:33:44:55", hostname: "iPhone", likelySame: "02:4e:58:00:00:03" };
  assert.equal(pendingText(d, name), "iPhone (06:11:22:33:44:55). It may be iPhone with a new address: the name the device gives itself says so");
  assert.equal(pendingText({ mac: "06:11:22:33:44:55", hostname: "" }, name), "06:11:22:33:44:55");
});

test("Telegram's problem is shown beside the alerts", () => {
  const st: AlertsState = { alerts: [], telegram: { ready: false, problem: "Telegram is off in Hermes: alerts stay in the console" }, countryTriesSeen: true,
    settings: { newDevice: true, blockedCountry: true, upload: true, channel: "telegram", quarantine: true } };
  assert.equal(telegramText(st), "Telegram: Telegram is off in Hermes: alerts stay in the console");
  assert.equal(telegramText({ ...st, telegram: { ready: true, problem: "" } }), "Telegram: alerts go to the Hermes bot");
  assert.deepEqual(alertsBody({ ...st.settings, channel: "pigeon" as "telegram" }), { newDevice: true, blockedCountry: true, upload: true, channel: "telegram" });
});

test("#/security is the security screen", () => {
  assert.deepEqual(parseRoute("#/security"), { name: "security" });
});

test("the Alerts tile counts the day's alerts and says when some were not sent", () => {
  const now = Date.parse("2026-11-21T12:00:00Z");
  const a = (id: string, at: string, sent = "telegram"): Alert => ({ id, kind: "new_device", at, sent });
  const st: AlertsState = { alerts: [a("1", "2026-11-21T11:00:00Z"), a("2", "2026-11-21T08:00:00Z", ""), a("3", "2026-11-19T11:00:00Z")],
    telegram: { ready: true, problem: "" }, countryTriesSeen: true,
    settings: { newDevice: true, blockedCountry: true, upload: true, channel: "telegram", quarantine: true } };
  assert.deepEqual(alertsTile(st, now), { value: "2", sub: "in 24 h · 1 not sent" });
  assert.deepEqual(alertsTile({ ...st, alerts: [] }, now), { value: "0", sub: "in 24 h" });
  assert.deepEqual(alertsTile(undefined, now), { value: "-", sub: "no data" });
});

test("the quarantine says what it does now, not only what is set", () => {
  setZone("Europe/London");
  const base: AlertsState = { alerts: [], telegram: { ready: true, problem: "" }, countryTriesSeen: true,
    settings: { newDevice: true, blockedCountry: true, upload: true, channel: "telegram", quarantine: true },
    quarantineState: { active: true, graceEnds: null, problem: "" } };
  assert.equal(quarantineText(base), "Quarantine is on: new devices cannot reach the house until you approve them");
  assert.equal(quarantineText({ ...base, quarantineState: { active: false, graceEnds: Date.parse("2026-11-22T12:05:00Z") / 1000, problem: "" } }),
    "First day: new devices join the known ones, quarantine starts at 12:05 on 22 Nov 2026");
  assert.equal(quarantineText({ ...base, quarantineState: { active: false, graceEnds: null, problem: "turned off, new devices reach the house at once" } }),
    "Quarantine is off: turned off, new devices reach the house at once");
});
