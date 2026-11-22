import { test } from "node:test";
import assert from "node:assert/strict";
import { entityHref, withFrom, crumbsFrom, filterHash, stretchHref, parentsOf } from "./entity";
import { parseRoute } from "./query";

test("every kind of name leads to its own screen", () => {
  assert.equal(entityHref({ kind: "client", mac: "aa:bb:cc:dd:ee:01" }), "#/client/aa:bb:cc:dd:ee:01");
  assert.equal(entityHref({ kind: "app", id: "youtube" }), "#/app/youtube");
  assert.equal(entityHref({ kind: "app", id: "other" }), undefined, "the folded rest leads nowhere");
  assert.equal(entityHref({ kind: "country", cc: "DE" }), "#/destination/DE");
  assert.equal(entityHref({ kind: "country", cc: "--" }), undefined);
  assert.equal(entityHref({ kind: "country", cc: "LAN" }), undefined);
  assert.equal(entityHref({ kind: "network", asn: 15169 }), "#/asn/15169");
  assert.equal(entityHref({ kind: "network", asn: 0 }), undefined);
  assert.equal(entityHref({ kind: "domain", name: "www.youtube.com" }), "#/domain/www.youtube.com");
  assert.equal(entityHref({ kind: "domain", name: "" }), undefined);
  assert.equal(entityHref({ kind: "category", id: "video" }), "#/apps?cat=video");
  assert.equal(entityHref({ kind: "group", id: "kids" }), "#/group/kids");
  assert.equal(entityHref({ kind: "rule", id: "r_1" }), "#/rules?rule=r_1");
});

test("a link from a detail carries where it came from, for the breadcrumbs", () => {
  assert.equal(withFrom("#/client/aa:bb:cc:dd:ee:01", "app:youtube"), "#/client/aa:bb:cc:dd:ee:01?from=app%3Ayoutube");
  assert.equal(withFrom("#/apps?cat=video", "group:kids"), "#/apps?cat=video&from=group%3Akids");
  assert.equal(withFrom("#/app/youtube", ""), "#/app/youtube");
});

test("the breadcrumbs name the path the owner took", () => {
  const names = { app: (id: string) => (id === "youtube" ? "YouTube" : id), group: (id: string) => (id === "kids" ? "Kids" : id) };
  assert.deepEqual(crumbsFrom("app:youtube", names), [{ label: "Apps", href: "#/apps" }, { label: "YouTube", href: "#/app/youtube" }]);
  assert.deepEqual(crumbsFrom("dest:DE", names), [{ label: "Destinations", href: "#/destinations" }, { label: "Germany", href: "#/destination/DE" }]);
  assert.deepEqual(crumbsFrom("asn:15169", names), [{ label: "Destinations", href: "#/destinations" }, { label: "AS15169", href: "#/asn/15169" }]);
  assert.deepEqual(crumbsFrom("group:kids", names), [{ label: "Clients", href: "#/clients" }, { label: "Kids", href: "#/group/kids" }]);
  assert.deepEqual(crumbsFrom(undefined, names), []);
  assert.deepEqual(crumbsFrom("evil:<x>", names), [], "only known places");
});

test("the new screens and the filters in the address", () => {
  assert.deepEqual(parseRoute("#/asn/15169"), { name: "asn", asn: 15169, from: "" });
  assert.deepEqual(parseRoute("#/asn/x"), { name: "destinations" });
  assert.deepEqual(parseRoute("#/domain/www.youtube.com"), { name: "domain", domain: "www.youtube.com", from: "" });
  assert.deepEqual(parseRoute("#/group/kids"), { name: "group", id: "kids", from: "" });
  assert.deepEqual(parseRoute("#/apps?cat=video&q=you"), { name: "apps", cat: "video", q: "you" });
  assert.deepEqual(parseRoute("#/clients?q=phone"), { name: "clients", q: "phone" });
  assert.deepEqual(parseRoute("#/client/aa:bb:cc:dd:ee:01?from=app%3Ayoutube"), { name: "client", mac: "aa:bb:cc:dd:ee:01", from: "app:youtube" });
  assert.deepEqual(parseRoute("#/flows?q=x&from=2026-11-22T20:00:00Z&to=2026-11-22T20:10:00Z"), { name: "flows", q: "x", from: "2026-11-22T20:00:00Z", to: "2026-11-22T20:10:00Z" });
  assert.deepEqual(parseRoute("#/rules?rule=r_1"), { name: "rules", rule: "r_1" });
});

test("a filter is written into the address, so Back and a shared link keep it", () => {
  assert.equal(filterHash("apps", { cat: "video", q: "you" }), "#/apps?cat=video&q=you");
  assert.equal(filterHash("apps", { cat: "", q: "" }), "#/apps");
  assert.equal(filterHash("clients", { q: "Sam's phone" }), "#/clients?q=Sam%27s+phone");
});

test("a click on a stretch of a chart asks the flows of that stretch", () => {
  assert.equal(stretchHref("2026-11-22T20:00:00Z", 600), "#/flows?from=2026-11-22T20%3A00%3A00Z&to=2026-11-22T20%3A10%3A00Z");
  assert.equal(stretchHref("not a time", 600), undefined);
});

test("a thing's view names what it belongs to, each a link", () => {
  assert.deepEqual(parentsOf({ kind: "app", id: "youtube" }, { app: { id: "youtube", name: "YouTube", category: "video" } }), [{ label: "Video", href: "#/apps?cat=video" }]);
  assert.deepEqual(parentsOf({ kind: "app", id: "x" }, { app: { id: "x", name: "X", category: "other" } }), []);
  assert.deepEqual(parentsOf({ kind: "network", asn: 13335 }, { countries: [{ cc: "US" }, { cc: "DE" }, { cc: "--" }] }), [
    { label: "United States", href: "#/destination/US" },
    { label: "Germany", href: "#/destination/DE" },
  ]);
  assert.deepEqual(parentsOf({ kind: "domain", name: "a.example" }, { apps: [{ app: { id: "youtube", name: "YouTube" } }, { app: { id: "unknown", name: "unknown" } }] }), [{ label: "YouTube", href: "#/app/youtube" }]);
  assert.deepEqual(parentsOf({ kind: "client", mac: "aa:bb:cc:dd:ee:01" }, { group: "kids" }, { group: () => "Kids" }), [{ label: "Kids", href: "#/group/kids" }]);
  assert.deepEqual(parentsOf({ kind: "client", mac: "aa:bb:cc:dd:ee:01" }, { group: "" }), []);
  assert.deepEqual(parentsOf({ kind: "country", cc: "US" }, {}), []);
  assert.deepEqual(parentsOf({ kind: "app", id: "youtube" }, undefined), []);
});
