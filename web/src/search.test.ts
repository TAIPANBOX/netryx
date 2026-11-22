import { test } from "node:test";
import assert from "node:assert/strict";
import { buildIndex, searchHits } from "./search";
import type { Device, Group, HouseApp, HouseDestinations } from "./types";
import type { Rule } from "./rules";

const dev = (o: Partial<Device>): Device => ({
  mac: "aa:bb:cc:dd:ee:01", name: "", hostname: "", vendor: "", label: "", group: "", random: false, ipv4: [], ipv6: [],
  link: { kind: "wifi", iface: "", ssid: "", band: "", signal: 0 } as Device["link"], first: "", last: "", online: true,
  rate: { up: 0, down: 0 }, last24h: { down: 0, up: 0 }, topApp: null, countryCount: 0, ...o,
}) as Device;
const app = (id: string, name: string, category: string): HouseApp => ({ app: { id, name, category }, down: 0, up: 0, flows: 0, share: 0, clients: 0, top: [] }) as HouseApp;

const index = buildIndex({
  devices: [dev({ mac: "aa:bb:cc:dd:ee:01", label: "Sam's phone", hostname: "sams-iphone", ipv4: ["192.168.1.23"] }), dev({ mac: "aa:bb:cc:dd:ee:02", name: "Work laptop" })],
  apps: [app("youtube", "YouTube", "video"), app("netflix", "Netflix", "video"), app("other", "other", "")],
  dest: {
    countries: [{ cc: "US" }, { cc: "--" }],
    networks: [{ cc: "US", asn: "13335", org: "CLOUDFLARENET - Cloudflare, Inc." }],
    domains: [{ name: "www.youtube.com", app: null }],
  } as unknown as HouseDestinations,
  groups: [{ id: "kids", name: "Kids", members: [] }] as Group[],
  rules: [{ id: "r_1", what: "app", targets: ["tiktok"], who: "group", group: "kids", members: [], days: [], from: "", to: "", until: null, action: "limit", minutes: 60 } as unknown as Rule],
  appName: (id) => (id === "tiktok" ? "TikTok" : id),
  people: [{ id: "alex", name: "Alex", macs: ["aa:bb:cc:dd:ee:02"] }],
});

test("a person is found by name", () => {
  assert.equal(searchHits("alex", index)[0].href, "#/person/alex");
});

test("typing a name finds the thing, best match first", () => {
  const hits = searchHits("you", index);
  assert.deepEqual(hits.map((h) => h.href), ["#/app/youtube", "#/domain/www.youtube.com"]);
  assert.equal(searchHits("sam", index)[0].href, "#/client/aa:bb:cc:dd:ee:01");
  assert.equal(searchHits("phone", index)[0].label, "Sam's phone"); // a word inside the name
  assert.equal(searchHits("cloudflare", index)[0].href, "#/asn/13335");
  assert.equal(searchHits("united", index)[0].href, "#/destination/US");
  assert.equal(searchHits("video", index)[0].href, "#/apps?cat=video");
  assert.equal(searchHits("kids", index)[0].href, "#/group/kids");
});

test("an address, a MAC, a hostname or an AS number finds its thing too", () => {
  assert.equal(searchHits("192.168.1.23", index)[0].label, "Sam's phone");
  assert.equal(searchHits("ee:02", index)[0].label, "Work laptop");
  assert.equal(searchHits("sams-iphone", index)[0].label, "Sam's phone");
  assert.equal(searchHits("13335", index)[0].href, "#/asn/13335");
  assert.equal(searchHits("AS13335", index)[0].href, "#/asn/13335");
});

test("a rule is found by what it names, and folded rows are not things", () => {
  assert.ok(searchHits("tiktok", index).some((h) => h.href === "#/rules?rule=r_1"));
  assert.equal(searchHits("other", index).length, 0);
  assert.equal(searchHits("--", index).length, 0);
  assert.equal(searchHits("", index).length, 0);
  assert.equal(searchHits("   ", index).length, 0);
});

test("the list is short and says what kind each thing is", () => {
  const many = buildIndex({ devices: Array.from({ length: 40 }, (_, i) => dev({ mac: `aa:bb:cc:dd:ee:${String(i).padStart(2, "0")}`, label: `Lamp ${i}` })), apps: [], dest: { countries: [], networks: [], domains: [] } as HouseDestinations, groups: [], rules: [] });
  const hits = searchHits("lamp", many);
  assert.equal(hits.length, 12);
  assert.equal(hits[0].kind, "client");
  assert.equal(hits[0].label, "Lamp 0");
});
