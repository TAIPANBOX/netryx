import { test } from "node:test";
import assert from "node:assert/strict";
import { EMPTY, openWin, closeWin, focusWin, topWin, moveWin, placeNear, MAX_WINDOWS, escCloses, cardSize, refit } from "./windows";

const view = { w: 1440, h: 900 };
const size = { w: 380, h: 420 };

test("a card opens beside what was clicked, inside the window", () => {
  assert.deepEqual(placeNear({ x: 100, y: 200, w: 120, h: 20 }, size, view), { x: 230, y: 200 }, "to the right of the name");
  assert.deepEqual(placeNear({ x: 1300, y: 200, w: 100, h: 20 }, size, view), { x: 910, y: 200 }, "to the left when the right has no room");
  assert.deepEqual(placeNear({ x: 100, y: 800, w: 120, h: 20 }, size, view), { x: 230, y: 468 }, "pulled up to stay on screen");
  assert.deepEqual(placeNear(null, size, view), { x: 530, y: 240 }, "centred without an anchor");
});

test("several cards stay open together, the last on top", () => {
  let s = openWin(EMPTY, "client:a", null, size, view);
  s = openWin(s, "client:b", null, size, view);
  assert.equal(s.wins.length, 2);
  assert.equal(topWin(s)?.key, "client:b");
});

test("opening a card that is open brings it forward instead of a second copy", () => {
  let s = openWin(EMPTY, "client:a", null, size, view);
  s = openWin(s, "client:b", null, size, view);
  s = openWin(s, "client:a", null, size, view);
  assert.equal(s.wins.length, 2);
  assert.equal(topWin(s)?.key, "client:a");
});

test("a click brings a card forward; X closes it; Esc closes the top one", () => {
  let s = openWin(EMPTY, "client:a", null, size, view);
  s = openWin(s, "client:b", null, size, view);
  const a = s.wins.find((w) => w.key === "client:a")!;
  s = focusWin(s, a.id);
  assert.equal(topWin(s)?.key, "client:a");
  s = closeWin(s, topWin(s)!.id);
  assert.deepEqual(s.wins.map((w) => w.key), ["client:b"]);
  s = closeWin(s, 999);
  assert.equal(s.wins.length, 1, "closing a card that is gone changes nothing");
});

test("a card is dragged but never off the screen", () => {
  let s = openWin(EMPTY, "client:a", null, size, view);
  const id = s.wins[0].id;
  s = moveWin(s, id, 50, 60, size, view);
  assert.deepEqual([s.wins[0].x, s.wins[0].y], [50, 60]);
  s = moveWin(s, id, 5000, -300, size, view);
  assert.deepEqual([s.wins[0].x, s.wins[0].y], [view.w - size.w - 12, 12]);
});

test("the number of open cards is bounded, the oldest closing first", () => {
  let s = EMPTY;
  for (let i = 0; i < MAX_WINDOWS + 2; i++) s = openWin(s, `client:${i}`, null, size, view);
  assert.equal(s.wins.length, MAX_WINDOWS);
  assert.ok(!s.wins.some((w) => w.key === "client:0" || w.key === "client:1"));
});

test("Esc closes a card, but not while the owner types in a field", () => {
  assert.equal(escCloses({ key: "Escape", defaultPrevented: false, target: { tagName: "DIV" } }), true);
  for (const tagName of ["INPUT", "TEXTAREA", "SELECT"]) {
    assert.equal(escCloses({ key: "Escape", defaultPrevented: false, target: { tagName } }), false, tagName);
  }
  assert.equal(escCloses({ key: "Escape", defaultPrevented: false, target: { tagName: "DIV", isContentEditable: true } }), false);
  assert.equal(escCloses({ key: "Escape", defaultPrevented: true, target: { tagName: "DIV" } }), false, "something else handled it");
  assert.equal(escCloses({ key: "Enter", defaultPrevented: false, target: { tagName: "DIV" } }), false);
});

test("a card is never wider than the window, and cards come back inside it when it shrinks", () => {
  assert.deepEqual(cardSize({ w: 1440, h: 900 }), { w: 380, h: 440 });
  assert.deepEqual(cardSize({ w: 375, h: 700 }), { w: 351, h: 440 });
  let s = openWin(EMPTY, "client:a", null, size, view);
  s = moveWin(s, s.wins[0].id, 1000, 400, size, view);
  const small = { w: 800, h: 600 };
  s = refit(s, cardSize(small), small);
  assert.deepEqual([s.wins[0].x, s.wins[0].y], [800 - 380 - 12, 600 - 440 - 12]);
});
