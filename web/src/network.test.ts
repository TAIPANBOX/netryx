import { test } from "node:test";
import assert from "node:assert/strict";
import { speedText, signalText, handshakeText, peerConnected, forwardTarget, shortKey } from "./network";
import { parseRoute } from "./query";

test("a port's speed in the unit a person reads", () => {
  assert.equal(speedText(0), "");
  assert.equal(speedText(100), "100 Mbit/s");
  assert.equal(speedText(1000), "1 Gbit/s");
  assert.equal(speedText(2500), "2.5 Gbit/s");
  assert.equal(speedText(10000), "10 Gbit/s");
});

test("a Wi-Fi signal in words", () => {
  assert.equal(signalText(0), "");
  assert.equal(signalText(-48), "-48 dBm, excellent");
  assert.equal(signalText(-60), "-60 dBm, good");
  assert.equal(signalText(-72), "-72 dBm, fair");
  assert.equal(signalText(-85), "-85 dBm, weak");
});

test("a peer's last handshake, and whether it is connected", () => {
  const now = Date.UTC(2026, 10, 22, 12, 0, 0);
  const ago = (s: number) => new Date(now - s * 1000).toISOString();
  assert.equal(handshakeText(null, now), "never");
  assert.equal(handshakeText(ago(20), now), "just now");
  assert.equal(handshakeText(ago(150), now), "2 min ago");
  assert.equal(handshakeText(ago(3 * 3600 + 5), now), "3 h ago");
  assert.equal(handshakeText(ago(2 * 86400 + 5), now), "2 days ago");
  assert.ok(peerConnected(ago(100), now), "WireGuard hands shake every two minutes while a peer is talking");
  assert.ok(!peerConnected(ago(200), now));
  assert.ok(!peerConnected(null, now));
});

test("a forward leads to a device by name, or to its address", () => {
  const f = { name: "Game", from: "wan", proto: "udp", port: "3074", destIp: "192.168.1.10", destPort: "3074", enabled: true, device: null };
  assert.equal(forwardTarget(f), "192.168.1.10:3074");
  assert.equal(forwardTarget({ ...f, device: { mac: "aa:bb:cc:dd:ee:01", name: "Laptop", online: true } }), "Laptop (192.168.1.10):3074");
  assert.equal(forwardTarget({ ...f, destIp: "" }), "the router:3074");
});

test("a public key is shortened, never a secret shown whole", () => {
  assert.equal(shortKey("cGhvbmUtcHVibGljLWtleS0wMDAwMDAwMDAwMDAwMDA9"), "cGhvbmUt…MDA9");
  assert.equal(shortKey(""), "");
});

test("the network route", () => {
  assert.deepEqual(parseRoute("#/network"), { name: "network" });
});
