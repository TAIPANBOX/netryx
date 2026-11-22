import { test } from "node:test";
import assert from "node:assert/strict";
import { createPoller, type Clock } from "./poll";

// A clock driven by hand: timers fire only when the test advances time.
function fakeClock() {
  let now = 0;
  let id = 0;
  const timers = new Map<number, { at: number; fn: () => void }>();
  const clock: Clock = {
    setTimeout: (fn, ms) => {
      timers.set(++id, { at: now + ms, fn });
      return id;
    },
    clearTimeout: (h) => void timers.delete(h as number),
  };
  const advance = async (ms: number) => {
    const until = now + ms;
    for (;;) {
      const due = [...timers.entries()].filter(([, t]) => t.at <= until).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) break;
      timers.delete(due[0]);
      now = due[1].at;
      due[1].fn();
      await flush();
    }
    now = until;
  };
  return { clock, advance, pending: () => timers.size };
}

const flush = () => new Promise<void>((r) => setImmediate(r));

// A load whose answers the test releases one at a time.
function slowLoads() {
  const calls: { signal: AbortSignal; resolve: (v: number) => void; reject: (e: Error) => void }[] = [];
  const load = (signal: AbortSignal) =>
    new Promise<number>((resolve, reject) => calls.push({ signal, resolve, reject }));
  return { load, calls };
}

test("a slow answer is waited for: no second request while the first is in flight, and none is aborted", async () => {
  const { clock, advance } = fakeClock();
  const { load, calls } = slowLoads();
  const got: number[] = [];
  const p = createPoller({ load, everyMs: 10_000, clock, hidden: () => false, onData: (v) => got.push(v), onError: () => {} });
  p.start();
  assert.equal(calls.length, 1);
  await advance(60_000); // a router that takes a minute to answer
  assert.equal(calls.length, 1, "a poll started while the last was still in flight");
  assert.equal(calls[0].signal.aborted, false, "the request in flight was aborted");
  calls[0].resolve(1);
  await flush();
  assert.deepEqual(got, [1]);
  await advance(9_999);
  assert.equal(calls.length, 1, "the next poll came before the interval after the answer");
  await advance(1);
  assert.equal(calls.length, 2);
  p.dispose();
});

test("an error is reported and polling carries on", async () => {
  const { clock, advance } = fakeClock();
  const { load, calls } = slowLoads();
  const errors: string[] = [];
  const p = createPoller({ load, everyMs: 5_000, clock, hidden: () => false, onData: () => {}, onError: (e) => errors.push(String(e)) });
  p.start();
  calls[0].reject(new Error("offline"));
  await flush();
  assert.deepEqual(errors, ["Error: offline"]);
  await advance(5_000);
  assert.equal(calls.length, 2);
  p.dispose();
});

test("a hidden tab polls nothing, and showing it loads at once", async () => {
  const { clock, advance, pending } = fakeClock();
  const { load, calls } = slowLoads();
  let hidden = false;
  const p = createPoller({ load, everyMs: 10_000, clock, hidden: () => hidden, onData: () => {}, onError: () => {} });
  p.start();
  calls[0].resolve(1);
  await flush();
  hidden = true;
  p.pause();
  assert.equal(pending(), 0, "a timer is left running while hidden");
  await advance(60_000);
  assert.equal(calls.length, 1);
  hidden = false;
  p.resume();
  assert.equal(calls.length, 2);
  p.dispose();
});

test("hiding during a request lets it finish but schedules nothing; showing again loads at once", async () => {
  const { clock, advance, pending } = fakeClock();
  const { load, calls } = slowLoads();
  let hidden = false;
  const got: number[] = [];
  const p = createPoller({ load, everyMs: 10_000, clock, hidden: () => hidden, onData: (v) => got.push(v), onError: () => {} });
  p.start();
  hidden = true;
  p.pause();
  calls[0].resolve(7);
  await flush();
  assert.deepEqual(got, [7], "an answer that arrived while hidden was thrown away");
  assert.equal(pending(), 0);
  await advance(30_000);
  assert.equal(calls.length, 1);
  hidden = false;
  p.resume();
  assert.equal(calls.length, 2);
  // Showing the tab while a request is in flight does not start a second.
  p.pause();
  p.resume();
  assert.equal(calls.length, 2);
  p.dispose();
});

test("disposing aborts the request in flight and drops its answer", async () => {
  const { clock, advance } = fakeClock();
  const { load, calls } = slowLoads();
  const got: number[] = [];
  const p = createPoller({ load, everyMs: 10_000, clock, hidden: () => false, onData: (v) => got.push(v), onError: () => {} });
  p.start();
  p.dispose();
  assert.equal(calls[0].signal.aborted, true);
  calls[0].resolve(1);
  await flush();
  await advance(60_000);
  assert.deepEqual(got, []);
  assert.equal(calls.length, 1);
});

test("a one-shot poller loads once and does not reload when shown again", async () => {
  const { clock, advance } = fakeClock();
  const { load, calls } = slowLoads();
  const p = createPoller({ load, everyMs: 0, clock, hidden: () => false, onData: () => {}, onError: () => {} });
  p.start();
  calls[0].resolve(1);
  await flush();
  await advance(60_000);
  p.pause();
  p.resume();
  assert.equal(calls.length, 1);
  p.dispose();
});

test("a poller started in a hidden tab waits to be shown", async () => {
  const { clock } = fakeClock();
  const { load, calls } = slowLoads();
  let hidden = true;
  const p = createPoller({ load, everyMs: 10_000, clock, hidden: () => hidden, onData: () => {}, onError: () => {} });
  p.start();
  assert.equal(calls.length, 0);
  hidden = false;
  p.resume();
  assert.equal(calls.length, 1);
  p.dispose();
});

test("a page that turns still between polls asks no more until it moves again", async () => {
  const { clock, advance } = fakeClock();
  let calls = 0;
  let still = false;
  const p = createPoller<number>({
    load: async () => ++calls,
    everyMs: 1000,
    clock,
    hidden: () => still,
    onData: () => {},
    onError: () => {},
  });
  p.start();
  await flush();
  assert.equal(calls, 1);
  still = true; // paused after the first answer, with no event to say so
  await advance(5000);
  assert.equal(calls, 1, "no poll while still");
  still = false;
  p.resume();
  await flush();
  assert.equal(calls, 2, "a poll as soon as it moves");
});
