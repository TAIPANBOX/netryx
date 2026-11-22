// Fetching from the router's API, and polling that stops while the tab is
// hidden: a console left open in a background tab must not keep the router busy.

import { useEffect, useRef, useState } from "preact/hooks";
import { createPoller } from "./poll";
import { isPaused, onPause } from "./pause";
import { setZone } from "./zone";

export class ApiError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

// The session's token for changes (X-Netryx-CSRF), kept in memory only: it is
// handed out by GET /api/session and POST /api/login and never stored.
let csrf = "";
export function setCSRF(token: string) {
  csrf = token;
}

// A 401 from any route means the session is gone: whoever shows the login
// listens here. The request that met it is not repeated.
const unauthorized = new Set<() => void>();
export function onUnauthorized(fn: () => void): () => void {
  unauthorized.add(fn);
  return () => {
    unauthorized.delete(fn);
  };
}
const sessionGone = () => unauthorized.forEach((fn) => fn());

async function failure(r: Response): Promise<ApiError> {
  let msg = `HTTP ${r.status}`;
  try {
    const j = (await r.json()) as { error?: string };
    if (j && typeof j.error === "string") msg = j.error;
  } catch {
    // the body was not JSON; the status says enough
  }
  return new ApiError(r.status, msg);
}

/** GET a same-origin JSON route. Nothing is ever fetched from anywhere else. */
export async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(path, { signal, cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } });
  noteBuild(r);
  if (!r.ok) {
    if (r.status === 401) sessionGone();
    throw await failure(r);
  }
  return (await r.json()) as T;
}

// The router's own sentences are shown as they are; a bare code from the
// login layer ("forbidden", "json") is put into words.
const CODES = new Set(["forbidden", "no-rpc", "login", "rpc", "rate", "json", "bad login", "method not allowed", "internal error", ""]);
function inWords(status: number, msg: string): string {
  if (!CODES.has(msg)) return msg;
  if (status === 403) return "No right to make changes: write access is needed.";
  if (status === 429) return "Too many requests, try again a little later.";
  if (status === 503) return "The router cannot make the change right now.";
  if (status >= 500) return "The router could not make the change.";
  return "The request was not accepted.";
}

/**
 * POST, PUT or DELETE a same-origin JSON route with the session's token. A 401
 * sends the page to the login and the write is not repeated: the person
 * presses the button again once signed in.
 */
export async function sendJSON<T>(method: "POST" | "PUT" | "DELETE", path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json", "X-Netryx-CSRF": csrf };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const r = await fetch(path, {
    method,
    cache: "no-store",
    credentials: "same-origin",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!r.ok) {
    if (r.status === 401) sessionGone();
    const e = await failure(r);
    if (e.status === 403) {
      // Ask the session again: it hands out the current token. If it still
      // may write, the 403 was the token and one more press will work.
      let s: Session | null | undefined;
      try {
        s = await getSession();
      } catch {
        // the router is not answering: the session is kept and the plain text below stands
      }
      if (s === null) sessionGone(); // a 401: the session really is gone
      else if (s?.canWrite) throw new ApiError(403, REFRESHED);
    }
    throw new ApiError(e.status, inWords(e.status, e.message));
  }
  try {
    return (await r.json()) as T;
  } catch {
    return undefined as T;
  }
}

export interface Session { user: string; csrf: string; canWrite: boolean; noRPC?: boolean; zone?: string }

/** The session behind the cookie, or null when there is none. Keeps its token for the writes. */
export async function getSession(): Promise<Session | null> {
  const r = await fetch("/api/session", { cache: "no-store", credentials: "same-origin", headers: { Accept: "application/json" } });
  noteBuild(r);
  if (r.status === 401) return null;
  if (!r.ok) throw await failure(r);
  const s = (await r.json()) as Session;
  setCSRF(s.csrf || "");
  setZone(s.zone);
  return s;
}

export type LoginResult = { ok: true; user: string; canWrite: boolean } | { ok: false; text: string };

/** A 403 on a write: the token may simply be stale (the router restarted its secret, or the session was renewed). */
const REFRESHED = "The session was refreshed, press again.";

/** POST /api/login. A wrong password is an answer for the form, not a lost session. */
export async function login(user: string, password: string): Promise<LoginResult> {
  let r: Response;
  try {
    r = await fetch("/api/login", {
      method: "POST",
      cache: "no-store",
      credentials: "same-origin",
      headers: { Accept: "application/json", "Content-Type": "application/json" },
      body: JSON.stringify({ user, password }),
    });
  } catch {
    return { ok: false, text: "The router is not answering." };
  }
  if (r.ok) {
    const j = (await r.json()) as { csrf: string; user: string; canWrite: boolean; zone?: string };
    setCSRF(j.csrf || "");
    setZone(j.zone);
    return { ok: true, user: j.user, canWrite: !!j.canWrite };
  }
  switch (r.status) {
    case 401:
      return { ok: false, text: "Wrong user name or password." };
    case 403:
      return {
        ok: false,
        text: "This user has no access to netryx. In /etc/config/rpcd its login needs list read 'netryx' (and list write 'netryx' to change things); an option line is not read as a grant.",
      };
    case 429: {
      const s = parseInt(r.headers.get("Retry-After") || "", 10);
      return { ok: false, text: s > 0 ? `Too many attempts. Try again in ${s} s.` : "Too many attempts. Try again a little later." };
    }
    case 503:
      return { ok: false, text: "Sign-in unavailable: rpcd is not answering." };
    default:
      return { ok: false, text: "Sign-in failed." };
  }
}

/** Ends the session; the page then shows the login whatever the router answers. */
export async function logout(): Promise<void> {
  try {
    await sendJSON("POST", "/api/logout");
  } catch {
    // the cookie is dropped by the page below either way
  }
  setCSRF("");
  sessionGone();
}

// A MAC is hex and colons, safe in a path as it is; anything else is encoded.
export const mac = (m: string) => (/^[0-9a-f:]+$/i.test(m) ? m : encodeURIComponent(m));

export interface Polled<T> {
  data?: T;
  error?: string;
  /** Status of the last failure, so a screen can tell "not found" from "offline". */
  status?: number;
  /** When data last arrived, in ms since the epoch. */
  at?: number;
}

/**
 * Runs `load` now and then `everyMs` after each answer (once if 0), through
 * createPoller: one request at a time, none while the page is hidden, and an
 * immediate load when it is shown. A change of deps or unmounting aborts the
 * request in flight. Data from a failed poll is kept, so the screen shows the
 * last good figures beside the error.
 */
export function usePoll<T>(load: (signal: AbortSignal) => Promise<T>, everyMs: number, deps: unknown[]): Polled<T> {
  const [state, setState] = useState<Polled<T>>({});
  const loadRef = useRef(load);
  loadRef.current = load;

  // Pause holds the figures still: no poll asks again while it is on, except
  // a screen's first load, so a screen opened while paused still shows data.
  const hasData = useRef(false);
  hasData.current = state.data !== undefined;
  useEffect(() => {
    const hidden = () => document.visibilityState === "hidden" || (isPaused() && hasData.current);
    const poller = createPoller<T>({
      load: (signal) => loadRef.current(signal),
      everyMs,
      clock: { setTimeout: (fn, ms) => window.setTimeout(fn, ms), clearTimeout: (h) => window.clearTimeout(h as number) },
      hidden,
      onData: (data) => setState({ data, at: Date.now() }),
      onError: (e) => {
        const status = e instanceof ApiError ? e.status : 0;
        const error = e instanceof Error ? e.message : String(e);
        setState((s) => ({ ...s, error, status }));
      },
    });
    const onVisibility = () => (hidden() ? poller.pause() : poller.resume());
    poller.start();
    document.addEventListener("visibilitychange", onVisibility);
    const offPause = onPause(onVisibility);
    return () => {
      poller.dispose();
      document.removeEventListener("visibilitychange", onVisibility);
      offPause();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [everyMs, ...deps]);

  return state;
}

// The build of this page, written in by build.sh; "dev" in tests and in a
// bundle built without it.
declare const __NETRYX_BUILD__: string;
let build = typeof __NETRYX_BUILD__ === "string" ? __NETRYX_BUILD__ : "dev";
let newBuildTold = false;
const newBuildListeners = new Set<() => void>();

/** Sets this page's build (tests). */
export function setBuild(id: string) {
  build = id;
  newBuildTold = false;
}

/**
 * Calls fn once when the router answers with a newer page than this one; at
 * once if that answer came before fn listened (the session is read before the
 * header that shows the notice is drawn).
 */
export function onNewBuild(fn: () => void): () => void {
  if (newBuildTold) {
    fn();
    return () => {};
  }
  newBuildListeners.add(fn);
  return () => newBuildListeners.delete(fn);
}

/** Reads the build the router serves from an answer, and tells once when it is not this page's. */
function noteBuild(r: Response) {
  const served = r.headers.get("X-Netryx-Build");
  if (!served || build === "dev" || served === build || newBuildTold) return;
  newBuildTold = true;
  for (const fn of newBuildListeners) fn();
}
