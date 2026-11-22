import { test } from "node:test";
import assert from "node:assert/strict";
import { rulesForApp, rulesForCountry, clientsLine, matchApp, categoriesIn, appHref, countryHref, domainText } from "./house";
import { parseRoute } from "./query";
import type { Rule } from "./rules";
import type { HouseApp } from "./types";

const rule = (p: Partial<Rule>): Rule => ({
  id: "r", enabled: true, what: "app", targets: [], who: "all", members: [], group: "", days: [], from: "", to: "",
  until: null, activeNow: true, hits: 0, applied: true, problem: "", error: "", ...p,
});

test("an app's rules are the ones that name it or its category", () => {
  const rules = [
    rule({ id: "a", what: "app", targets: ["youtube", "netflix"] }),
    rule({ id: "b", what: "category", targets: ["video"] }),
    rule({ id: "c", what: "app", targets: ["tiktok"] }),
    rule({ id: "d", what: "country", targets: ["US"] }),
    rule({ id: "e", what: "all", who: "device", members: ["aa:bb:cc:dd:ee:01"] }),
  ];
  assert.deepEqual(rulesForApp(rules, { id: "netflix", category: "video" }).map((r) => r.id), ["a", "b"]);
  assert.deepEqual(rulesForApp(rules, { id: "github", category: "dev" }).map((r) => r.id), []);
  assert.deepEqual(rulesForApp(undefined, { id: "netflix", category: "video" }), []);
});

test("a country's rules are the country rules that name it", () => {
  const rules = [
    rule({ id: "a", what: "country", targets: ["CN", "RU"] }),
    rule({ id: "b", what: "country", targets: ["US"] }),
    rule({ id: "c", what: "app", targets: ["cn"] }),
  ];
  assert.deepEqual(rulesForCountry(rules, "RU").map((r) => r.id), ["a"]);
  assert.deepEqual(rulesForCountry(rules, "ru").map((r) => r.id), ["a"]);
  assert.deepEqual(rulesForCountry(rules, "DE"), []);
});

test("a row names its first clients and counts the rest", () => {
  const top = [
    { mac: "aa:bb:cc:dd:ee:01", name: "Laptop", down: 1, up: 1, other: false },
    { mac: "aa:bb:cc:dd:ee:02", name: "", down: 1, up: 1, other: false },
    { mac: "router", name: "router", down: 1, up: 1, other: true },
  ];
  assert.equal(clientsLine(0, []), "no clients");
  assert.equal(clientsLine(1, top.slice(0, 1)), "Laptop");
  assert.equal(clientsLine(2, top), "Laptop, aa:bb:cc:dd:ee:02");
  assert.equal(clientsLine(7, top), "Laptop, aa:bb:cc:dd:ee:02 and 5 more");
});

test("traffic with no client behind it says whose it is", () => {
  const router = { mac: "router", name: "router", down: 1, up: 1, other: true };
  const bare = { mac: "192.168.1.9", name: "192.168.1.9", down: 1, up: 1, other: true };
  assert.equal(clientsLine(0, [router]), "the router itself");
  assert.equal(clientsLine(0, [bare]), "devices without a hardware address");
  assert.equal(clientsLine(0, [router, bare]), "the router itself and devices without a hardware address");
});

test("a row without a name says so", () => {
  assert.equal(domainText({ name: "", other: false }), "no name");
  assert.equal(domainText({ name: "other", other: true }), "other names");
  assert.equal(domainText({ name: "zoom.us" }), "zoom.us");
});

const app = (id: string, name: string, category: string): HouseApp => ({
  app: { id, name, category }, down: 1, up: 1, flows: 1, share: 0, clients: 1, top: [], other: false,
});

test("the apps search matches the name and the category, any case", () => {
  const nf = app("netflix", "Netflix", "video");
  assert.ok(matchApp(nf, ""));
  assert.ok(matchApp(nf, "NET"));
  assert.ok(matchApp(nf, "video"));
  assert.ok(matchApp(app("chatgpt", "ChatGPT", "ai"), "assistants"), "the category's English name is searched too");
  assert.ok(!matchApp(nf, "git"));
});

test("the category pills list the categories present, busiest first", () => {
  const list = [app("a", "A", "video"), app("b", "B", "social"), app("c", "C", "video"), { ...app("o", "other", ""), other: true }];
  list[1].down = 100;
  assert.deepEqual(categoriesIn(list), ["social", "video"]);
});

test("rows link to their detail screens, the catch-alls nowhere", () => {
  assert.equal(appHref(app("netflix", "Netflix", "video")), "#/app/netflix");
  assert.equal(appHref({ ...app("other", "other", ""), other: true }), undefined);
  assert.equal(countryHref("DE"), "#/destination/DE");
  assert.equal(countryHref("--"), undefined);
});

test("the apps and destinations routes", () => {
  assert.deepEqual(parseRoute("#/apps"), { name: "apps" });
  assert.deepEqual(parseRoute("#/app/netflix"), { name: "app", id: "netflix" });
  assert.deepEqual(parseRoute("#/app/"), { name: "apps" });
  assert.deepEqual(parseRoute("#/app/%3Cx%3E"), { name: "apps" });
  assert.deepEqual(parseRoute("#/destinations"), { name: "destinations" });
  assert.deepEqual(parseRoute("#/destination/de"), { name: "destination", cc: "DE" });
  assert.deepEqual(parseRoute("#/destination/deu"), { name: "destinations" });
});
