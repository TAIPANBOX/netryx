import { setZone } from "./zone";
import { test } from "node:test";
import assert from "node:assert/strict";
import {
  bytes, rate, ratePair, share, num, time, date, dateTime, duration, band,
  category, appName, countryName, isZeroTime, macShort, plural, ms, pct, port, dirArrow, nameSource, groupName,
  clientName, newDevices, clock,
} from "./format";

const SEP = ","; // thousands are grouped with a comma

test("bytes are binary units with a point decimal", () => {
  assert.equal(bytes(1536), "1.5 KB");
  assert.equal(bytes(0), "0 B");
  assert.equal(bytes(512), "512 B");
  assert.equal(bytes(1023), "1023 B");
  assert.equal(bytes(38.4 * 1024 ** 3), "38.4 GB");
  assert.equal(bytes(14 * 1024 ** 2), "14 MB"); // a whole value drops its ".0"
  assert.equal(bytes(310 * 1024 ** 2), "310 MB"); // three digits carry no decimal
  assert.equal(bytes(1000 * 1024), "1000 KB"); // binary units step at 1024, not 1000
  assert.equal(bytes(1024 ** 2), "1 MB");
  assert.equal(bytes(2.5 * 1024 ** 4), "2.5 TB");
});

test("rates are bits per second in decimal units, from bytes per second", () => {
  assert.equal(rate(312e6 / 8), "312 Mbit/s");
  assert.equal(rate(0), "0 bit/s");
  assert.equal(rate(125), "1 kbit/s");
  assert.equal(rate(41.25e6 / 8), "41.3 Mbit/s");
  assert.equal(rate(1.5e9 / 8), "1.5 Gbit/s");
});

test("a rate pair shares one unit, chosen by the larger side", () => {
  assert.deepEqual(ratePair(312e6 / 8, 41e6 / 8), { down: "312", up: "41", unit: "Mbit/s" });
  assert.deepEqual(ratePair(1e3 / 8, 5e6 / 8), { down: "0", up: "5", unit: "Mbit/s" });
  assert.deepEqual(ratePair(0, 0), { down: "0", up: "0", unit: "bit/s" });
});

test("shares are whole percents, and a sliver is not shown as nothing", () => {
  assert.equal(share(0.214), "21%");
  assert.equal(share(0), "0%");
  assert.equal(share(1), "100%");
  assert.equal(share(0.003), "<1%");
  assert.equal(share(0.996), "100%");
});

test("numbers are grouped by threes with a comma", () => {
  assert.equal(num(1418), `1${SEP}418`);
  assert.equal(num(0), "0");
  assert.equal(num(999), "999");
  assert.equal(num(1234567), `1${SEP}234${SEP}567`);
  assert.equal(num(-1500), `-1${SEP}500`);
});

test("times are in the zone the router names", () => {
  setZone("Europe/London");
  try {
    assert.equal(time("2026-11-20T12:05:00Z"), "12:05"); // GMT
    assert.equal(time("2026-07-01T12:05:00Z"), "13:05"); // BST
    assert.equal(time("2026-07-01T23:30:00Z"), "00:30"); // the next day in London
    assert.equal(date("2026-03-02T10:00:00Z"), "2 Mar 2026");
    assert.equal(date("2026-06-30T23:30:00Z"), "1 Jul 2026");
    assert.equal(dateTime("2026-11-20T21:14:06Z"), "20 Nov 2026 21:14:06");
    assert.equal(clock("2026-07-01T14:42:18Z"), "15:42:18"); // the live card's "last update at"
    assert.equal(clock(""), "-");
  } finally {
    setZone("UTC");
  }
});

test("an absent time reads as a dash, never as year one", () => {
  assert.equal(isZeroTime("0001-01-01T00:00:00Z"), true);
  assert.equal(isZeroTime(""), true);
  assert.equal(isZeroTime("2026-11-20T12:05:00Z"), false);
  assert.equal(time("0001-01-01T00:00:00Z"), "-");
  assert.equal(time("not a time"), "-");
  assert.equal(date(""), "-");
});

test("durations name the two largest units", () => {
  assert.equal(duration(0), "0 s");
  assert.equal(duration(20), "20 s");
  assert.equal(duration(41 * 60), "41 min");
  assert.equal(duration(2 * 3600 + 10 * 60), "2 h 10 min");
  assert.equal(duration(3600), "1 h");
  assert.equal(duration(12 * 86400 + 4 * 3600 + 59), "12 d 4 h");
  assert.equal(duration(-5), "0 s");
});

test("bands, categories and app names are said in Ukrainian", () => {
  assert.equal(band("5 GHz"), "5 GHz");
  assert.equal(band("2.4 GHz"), "2.4 GHz");
  assert.equal(band("5g"), "5 GHz");
  assert.equal(band(""), "");
  assert.equal(category("video"), "Video");
  assert.equal(category("calls"), "Video calls");
  assert.equal(category("unknown"), "Unknown");
  assert.equal(category("travel"), "Travel");
  assert.equal(category("payments"), "Payments");
  assert.equal(category("smarthome"), "Smart home");
  assert.equal(category("dev"), "Development");
  assert.equal(category("lan"), "Local network");
  assert.equal(category("brand-new"), "brand-new"); // unknown ids pass through
  assert.equal(category(""), "");
  assert.equal(appName("Unknown"), "Unknown");
  assert.equal(appName("Unknown · Akamai"), "Unknown · Akamai");
  assert.equal(appName("Netflix"), "Netflix");
  assert.equal(appName(""), "Unknown");
});

test("country names fall back to the code", () => {
  assert.equal(countryName("GB"), "United Kingdom");
  assert.equal(countryName("--"), "--");
  assert.equal(countryName(""), "");
  assert.equal(countryName("ZZZ"), "ZZZ");
});

test("a MAC is shortened to its vendor half for a crowded line", () => {
  assert.equal(macShort("24:0a:c4:11:22:33"), "24:0A:C4:…");
  assert.equal(macShort("junk"), "junk");
});

test("counts agree with their noun", () => {
  const c = (n: number) => plural(n, "client", "clients");
  assert.equal(c(1), "1 client");
  assert.equal(c(2), "2 clients");
  assert.equal(c(11), "11 clients");
  assert.equal(c(21), "21 clients"); // English has no "21 client"
  assert.equal(c(0), "0 clients");
  assert.equal(c(-1), "-1 client");
  assert.equal(c(1418), `1${SEP}418 clients`);
});

test("line quality is said in milliseconds and percents with a comma", () => {
  assert.equal(ms(12.5), "12.5 ms");
  assert.equal(ms(9), "9 ms");
  assert.equal(pct(0), "0%");
  assert.equal(pct(2.25), "2.3%");
});

test("a flow is described by its port, direction and where its name came from", () => {
  assert.equal(port("203.0.113.5:443", 6), "tcp/443");
  assert.equal(port("[2001:db8::1]:8801", 17), "udp/8801");
  assert.equal(port("198.51.100.1", 1), "icmp");
  assert.equal(port("198.51.100.1:0", 47), "47");
  assert.equal(dirArrow("out"), "↑");
  assert.equal(dirArrow("in"), "↓");
  assert.equal(dirArrow("local"), "⇄");
  assert.equal(nameSource("sni"), "SNI");
  assert.equal(nameSource("quic"), "QUIC");
  assert.equal(nameSource("dns"), "DNS");
  assert.equal(nameSource("ech"), "hidden (ECH)");
  assert.equal(nameSource("none"), "no name");
  assert.equal(nameSource(""), "");
});

test("the house's own traffic and the router are named, not coded", () => {
  assert.equal(countryName("LAN"), "Local network");
  assert.equal(clientName({ mac: "router", name: "router" }), "Router");
  assert.equal(clientName({ mac: "aa:bb:cc:dd:ee:ff", name: "Laptop" }), "Laptop");
  assert.equal(clientName({ mac: "aa:bb:cc:dd:ee:ff", name: "" }), "aa:bb:cc:dd:ee:ff");
});

test("new devices are counted only after a day of watching", () => {
  assert.equal(newDevices(null), "shown after a day");
  assert.equal(newDevices(2), "2 new in 24 h");
});

test("the same moment is read in whatever zone the router names, and UTC when it names none", () => {
  try {
    setZone("Africa/Nairobi");
    assert.equal(time("2026-11-20T21:30:00Z"), "00:30");
    assert.equal(date("2026-11-20T21:30:00Z"), "21 Nov 2026");
    setZone("Not/AZone");
    assert.equal(time("2026-07-01T12:05:00Z"), "12:05");
    setZone(undefined);
    assert.equal(time("2026-07-01T12:05:00Z"), "12:05");
  } finally {
    setZone("UTC");
  }
});

test("a device's group is shown by its name, not its id", () => {
  const groups = [{ id: "kids", name: "Kids" }, { id: "g2", name: "" }];
  assert.equal(groupName("kids", groups), "Kids");
  // A group the list does not know, or one with no name, keeps its id.
  assert.equal(groupName("gone", groups), "gone");
  assert.equal(groupName("g2", groups), "g2");
  assert.equal(groupName("", groups), "");
});
