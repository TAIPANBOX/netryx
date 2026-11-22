import { test } from "node:test";
import assert from "node:assert/strict";
import { sendJSON, setCSRF, onUnauthorized, ApiError, login, getJSON, setBuild, onNewBuild } from "./api";

interface Call { url: string; init: RequestInit }

function fakeFetch(status: number, body: unknown, headers: Record<string, string> = {}): { calls: Call[]; restore: () => void } {
  const calls: Call[] = [];
  const real = globalThis.fetch;
  globalThis.fetch = (async (url: string, init: RequestInit) => {
    calls.push({ url, init });
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });
  }) as typeof fetch;
  return { calls, restore: () => { globalThis.fetch = real; } };
}

test("a write carries the session's token and a JSON body", async () => {
  const f = fakeFetch(200, { id: "r_1" });
  try {
    setCSRF("tok123");
    const out = await sendJSON<{ id: string }>("POST", "/api/rules", { what: "all" });
    assert.deepEqual(out, { id: "r_1" });
    assert.equal(f.calls.length, 1);
    const h = f.calls[0].init.headers as Record<string, string>;
    assert.equal(h["X-Netryx-CSRF"], "tok123");
    assert.equal(h["Content-Type"], "application/json");
    assert.equal(f.calls[0].init.method, "POST");
    assert.equal(f.calls[0].init.body, '{"what":"all"}');
    assert.equal(f.calls[0].init.credentials, "same-origin");
    await sendJSON("DELETE", "/api/rules/r_1");
    assert.equal(f.calls[1].init.body, undefined);
    assert.equal(f.calls[1].init.method, "DELETE");
  } finally {
    f.restore();
  }
});

test("a 401 sends the page to the login and the write is not retried", async () => {
  const f = fakeFetch(401, { error: "login" });
  let sent = 0;
  const off = onUnauthorized(() => { sent++; });
  try {
    await assert.rejects(() => sendJSON("POST", "/api/allow", { minutes: 30 }), (e: unknown) => e instanceof ApiError && e.status === 401);
    assert.equal(sent, 1);
    assert.equal(f.calls.length, 1);
  } finally {
    off();
    f.restore();
  }
});

test("a refusal shows the router's Ukrainian text, and a bare English code is put into words", async () => {
  let f = fakeFetch(409, { error: "That name is taken." });
  try {
    await assert.rejects(() => sendJSON("POST", "/api/rules", {}), (e: unknown) => e instanceof ApiError && e.status === 409 && e.message === "That name is taken.");
  } finally {
    f.restore();
  }
  f = fakeFetch(403, { error: "forbidden" });
  try {
    await assert.rejects(() => sendJSON("POST", "/api/rules", {}), (e: unknown) => e instanceof ApiError && e.status === 403 && /write access/.test(e.message));
  } finally {
    f.restore();
  }
});

test("a wrong password is told on the form and does not reopen the login", async () => {
  const f = fakeFetch(401, { error: "login" });
  let sent = 0;
  const off = onUnauthorized(() => { sent++; });
  try {
    const r = await login("owner", "bad");
    assert.deepEqual(r, { ok: false, text: "Wrong user name or password." });
    assert.equal(sent, 0);
  } finally {
    off();
    f.restore();
  }
});

test("a rate limit says how long to wait; no rpcd says so", async () => {
  let f = fakeFetch(429, { error: "rate" }, { "Retry-After": "30" });
  try {
    const r = await login("owner", "x");
    assert.equal(r.ok, false);
    assert.match(r.ok ? "" : r.text, /30 s/);
  } finally {
    f.restore();
  }
  f = fakeFetch(503, { error: "no-rpc" });
  try {
    const r = await login("owner", "x");
    assert.equal(r.ok, false);
    assert.match(r.ok ? "" : r.text, /rpcd/);
  } finally {
    f.restore();
  }
});

test("a good login keeps the token for the writes that follow", async () => {
  const f = fakeFetch(200, { csrf: "newtok", user: "owner", canWrite: true });
  try {
    setCSRF("");
    const r = await login("owner", "pw");
    assert.deepEqual(r, { ok: true, user: "owner", canWrite: true });
    f.restore();
    const g = fakeFetch(200, {});
    await sendJSON("POST", "/api/allow", {});
    assert.equal((g.calls[0].init.headers as Record<string, string>)["X-Netryx-CSRF"], "newtok");
    g.restore();
  } finally {
    f.restore();
  }
});

function routedFetch(routes: Record<string, { status: number; body: unknown }>): { calls: string[]; restore: () => void } {
  const calls: string[] = [];
  const real = globalThis.fetch;
  globalThis.fetch = (async (url: string) => {
    calls.push(url);
    const r = routes[url] ?? { status: 404, body: {} };
    return new Response(JSON.stringify(r.body), { status: r.status, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  return { calls, restore: () => { globalThis.fetch = real; } };
}

test("a 403 on a write refreshes the token and asks for one more press when the session may still write", async () => {
  const f = routedFetch({
    "/api/rules": { status: 403, body: { error: "forbidden" } },
    "/api/session": { status: 200, body: { user: "root", csrf: "fresh", canWrite: true, zone: "UTC" } },
  });
  try {
    setCSRF("stale");
    await assert.rejects(() => sendJSON("POST", "/api/rules", {}), (e: unknown) => e instanceof ApiError && e.status === 403 && e.message === "The session was refreshed, press again.");
    assert.deepEqual(f.calls, ["/api/rules", "/api/session"]);
  } finally {
    f.restore();
  }
});

test("a 403 for a session that cannot write says so", async () => {
  const f = routedFetch({
    "/api/rules": { status: 403, body: { error: "forbidden" } },
    "/api/session": { status: 200, body: { user: "viewer", csrf: "t", canWrite: false } },
  });
  try {
    await assert.rejects(() => sendJSON("POST", "/api/rules", {}), (e: unknown) => e instanceof ApiError && /write access/.test(e.message));
  } finally {
    f.restore();
  }
});

test("a failed session refresh after a 403 is not a logout", async () => {
  const real = globalThis.fetch;
  let sent = 0;
  const off = onUnauthorized(() => { sent++; });
  globalThis.fetch = (async (url: string) => {
    if (url === "/api/session") throw new TypeError("network down");
    return new Response(JSON.stringify({ error: "forbidden" }), { status: 403, headers: { "Content-Type": "application/json" } });
  }) as typeof fetch;
  try {
    await assert.rejects(() => sendJSON("POST", "/api/rules", {}), (e: unknown) => e instanceof ApiError && e.status === 403 && /write access/.test(e.message));
    assert.equal(sent, 0);
  } finally {
    off();
    globalThis.fetch = real;
  }
  const f = routedFetch({
    "/api/rules": { status: 403, body: { error: "forbidden" } },
    "/api/session": { status: 401, body: { error: "login" } },
  });
  const off2 = onUnauthorized(() => { sent++; });
  try {
    await assert.rejects(() => sendJSON("POST", "/api/rules", {}));
    assert.equal(sent, 1, "a 401 from the session route is a logout");
  } finally {
    off2();
    f.restore();
  }
});

test("a login without the netryx grant says how rpcd wants it written", async () => {
  const real = globalThis.fetch;
  globalThis.fetch = (async () => new Response(JSON.stringify({ error: "forbidden" }), { status: 403, headers: { "Content-Type": "application/json" } })) as typeof fetch;
  try {
    const r = await login("lab", "x");
    assert.equal(r.ok, false);
    assert.match(!r.ok ? r.text : "", /list read 'netryx'/);
    assert.match(!r.ok ? r.text : "", /option/);
  } finally {
    globalThis.fetch = real;
  }
});

test("a page left open across an upgrade is told the router serves a newer one", async () => {
  setBuild("aaaaaaaaaaaa");
  let told = 0;
  const off = onNewBuild(() => told++);
  const f = fakeFetch(200, { ok: true }, { "X-Netryx-Build": "aaaaaaaaaaaa" });
  try {
    await getJSON("/api/overview");
    assert.equal(told, 0, "the same build is not news");
  } finally {
    f.restore();
  }
  const g = fakeFetch(200, { ok: true }, { "X-Netryx-Build": "bbbbbbbbbbbb" });
  try {
    await getJSON("/api/overview");
    await getJSON("/api/overview");
    assert.equal(told, 1, "a newer build is told once");
  } finally {
    g.restore();
    off();
  }
});
