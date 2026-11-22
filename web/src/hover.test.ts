import { test } from "node:test";
import assert from "node:assert/strict";
import { hoverEntity, hoverPath, hoverSummary } from "./hover";
import { bytes } from "./format";
const day = (d: number, u: number) => `24 h: ↓ ${bytes(d)} · ↑ ${bytes(u)}`;

test("a link's address says which thing its card shows, and where to read it", () => {
  assert.deepEqual(hoverEntity("#/client/aa:bb:cc:dd:ee:01?from=app:youtube"), { kind: "client", mac: "aa:bb:cc:dd:ee:01" });
  assert.deepEqual(hoverEntity("#/app/youtube"), { kind: "app", id: "youtube" });
  assert.deepEqual(hoverEntity("#/destination/US"), { kind: "country", cc: "US" });
  assert.deepEqual(hoverEntity("#/asn/13335?from=dest-root"), { kind: "network", asn: 13335 });
  assert.deepEqual(hoverEntity("#/domain/www.youtube.com"), { kind: "domain", name: "www.youtube.com" });
  assert.deepEqual(hoverEntity("#/group/kids"), { kind: "group", id: "kids" });
  assert.deepEqual(hoverEntity("#/rules?rule=r_1"), { kind: "rule", id: "r_1" });
  assert.deepEqual(hoverEntity("#/apps?cat=video"), { kind: "category", id: "video" });
  // Screens that are not one thing have no card.
  for (const h of ["#/", "#/clients", "#/apps", "#/apps?q=you", "#/rules", "#/flows?q=x", "elsewhere/page", "#/app/BAD ID"]) assert.equal(hoverEntity(h), null, h);

  assert.equal(hoverPath({ kind: "client", mac: "aa:bb:cc:dd:ee:01" }), "/api/devices/aa:bb:cc:dd:ee:01");
  assert.equal(hoverPath({ kind: "app", id: "youtube" }), "/api/apps/youtube?range=24h");
  assert.equal(hoverPath({ kind: "country", cc: "US" }), "/api/destinations/US?range=24h");
  assert.equal(hoverPath({ kind: "network", asn: 13335 }), "/api/networks/13335?range=24h");
  assert.equal(hoverPath({ kind: "domain", name: "a.example" }), "/api/domains/a.example?range=24h");
  assert.equal(hoverPath({ kind: "group", id: "kids" }), "/api/groups");
  assert.equal(hoverPath({ kind: "rule", id: "r_1" }), "/api/rules");
  assert.equal(hoverPath({ kind: "category", id: "video" }), "/api/apps?range=24h");
});

test("a card says the few things worth knowing before opening it", () => {
  const dev = { mac: "aa:bb:cc:dd:ee:01", label: "Sam's phone", name: "", hostname: "", online: true, ipv4: ["192.168.1.23"], last24h: { down: 2_000_000, up: 30_000 }, topApp: { id: "instagram", name: "Instagram", category: "social" }, countryCount: 3, group: "kids" };
  assert.deepEqual(hoverSummary({ kind: "client", mac: dev.mac }, dev), {
    title: "Sam's phone", lines: ["online · 192.168.1.23", day(2_000_000, 30_000), "busiest app: Instagram", "3 countries"],
  });
  assert.deepEqual(hoverSummary({ kind: "app", id: "youtube" }, { app: { id: "youtube", name: "YouTube", category: "video" }, down: 900_000_000, up: 11_000_000, share: 0.12, clientCount: 1 }), {
    title: "YouTube", lines: ["Video", day(900_000_000, 11_000_000), "12% of the house's traffic", "1 client"],
  });
  assert.deepEqual(hoverSummary({ kind: "network", asn: 13335 }, { asn: "13335", org: "Cloudflare", down: 8e9, up: 1e9, countries: [{ cc: "US" }], clients: [1, 2, 3, 4], apps: [1] }), {
    title: "Cloudflare", lines: ["AS13335 · reached in United States", day(8e9, 1e9), "4 clients · 1 app"],
  });
  assert.deepEqual(hoverSummary({ kind: "domain", name: "a.example" }, { name: "a.example", down: 5000, up: 100, apps: [{ app: { name: "YouTube" } }], clients: [1] }).lines, [day(5000, 100), "named for YouTube", "1 client"]);
  assert.deepEqual(hoverSummary({ kind: "country", cc: "US" }, { cc: "US", down: 1e9, up: 0, share: 0.97, clientCount: 6, networks: [1, 2] }).lines, ["97% of what left the house", day(1e9, 0), "6 clients · 2 networks"]);
  assert.deepEqual(hoverSummary({ kind: "group", id: "kids" }, [{ id: "kids", name: "Kids", members: ["a", "b"] }]), { title: "Kids", lines: ["group of 2 devices"] });
  assert.equal(hoverSummary({ kind: "group", id: "gone" }, []).lines[0], "this group no longer exists");
  assert.deepEqual(hoverSummary({ kind: "category", id: "video" }, [{ app: { category: "video" } }, { app: { category: "video" } }, { app: { category: "social" } }]), { title: "Video", lines: ["2 apps the house used in 24 h"] });
  const r = { id: "r_1", enabled: true, what: "app", targets: ["tiktok"], who: "all", members: [], group: "", days: [], from: "", to: "", until: null, activeNow: true, hits: 4, applied: true, problem: "", error: "", action: "block" };
  assert.deepEqual(hoverSummary({ kind: "rule", id: "r_1" }, [r]).lines, ["active now", "blocked 4 times"]);
  assert.equal(hoverSummary({ kind: "rule", id: "r_9" }, [r]).lines[0], "this rule no longer exists");
});
