// The buttons that change something: block a country or an app, pause a
// device, give it 30 more minutes, cut a connection. Each one states its limit
// where it sits, shows the router's own refusal beside itself, and (where
// that is safe) offers to take the change back. None is drawn without the
// right to write.

import { h, Fragment } from "preact";
import { useRef, useState } from "preact/hooks";
import { once } from "../guard";
import type { Device, LiveConn } from "../types";
import { getJSON, mac as enc, sendJSON, usePoll } from "../api";
import { useSession } from "../ui";
import { cutNote, type CutAnswer } from "../live";
import { countryName, time } from "../format";
import { activePause, appliesTo, pauseBody, stateOf, untilText, type PauseKind, type Rule, type RuleBody } from "../rules";

export const CDN_NOTE = "a service of this country reached through a CDN in another country is not blocked this way";
export const CUT_NOTE = "the app may connect again at once unless a rule stops it";

interface Shown { busy: boolean; err: string; note: string; undo: (() => Promise<unknown>) | null; link: boolean }
const IDLE: Shown = { busy: false, err: "", note: "", undo: null, link: false };

/**
 * One button's life: running, the router's refusal, what was done and how to
 * take it back. `run` returns the note to show (and optionally the undo).
 */
function useChange(after?: () => void) {
  const [s, set] = useState<Shown>(IDLE);
  // One change at a time: a second press before the page has redrawn is dropped.
  const flag = useRef(false);
  const run = (fn: () => Promise<{ note: string; undo?: () => Promise<unknown>; link?: boolean }>) =>
    once(flag, async () => {
      set({ ...IDLE, busy: true });
      try {
        const r = await fn();
        set({ ...IDLE, note: r.note, undo: r.undo ?? null, link: !!r.link });
        after?.();
      } catch (e) {
        set({ ...IDLE, err: e instanceof Error ? e.message : String(e) });
      }
    });
  const undo = () =>
    once(flag, async () => {
      const u = s.undo;
      if (!u) return;
      set({ ...s, busy: true, undo: null });
      try {
        await u();
        set({ ...IDLE, note: "Undone." });
        after?.();
      } catch (e) {
        set({ ...IDLE, err: e instanceof Error ? e.message : String(e) });
      }
    });
  return { s, run, undo, reset: () => set(IDLE) };
}

function Outcome({ s, undo }: { s: Shown; undo: () => void }) {
  return (
    <Fragment>
      {s.err && <span class="act-err" role="alert">{s.err}</span>}
      {s.note && (
        <span class="act-note" role="status">
          {s.note}
          {s.link && <Fragment> <a href="#/rules">Rules</a></Fragment>}
          {s.undo && <Fragment> <button type="button" class="link-btn" onClick={undo} disabled={s.busy}>Undo</button></Fragment>}
        </span>
      )}
    </Fragment>
  );
}

interface Created { id?: string; enabled?: boolean; activeNow?: boolean }

const createRule = async (body: RuleBody) => {
  const r = await sendJSON<Rule & Created>("POST", "/api/rules", body);
  const id = r?.id ?? "";
  const state = typeof r?.enabled === "boolean" ? stateOf(r as Rule, Date.now()).text : "";
  return {
    note: state ? `Rule created: ${state}.` : "Rule created.",
    link: true,
    undo: id ? () => sendJSON("DELETE", `/api/rules/${encodeURIComponent(id)}`) : undefined,
  };
};

/** A country row: close the country for everyone, after one confirmation. */
export function BlockCountry({ cc }: { cc: string }) {
  const { canWrite } = useSession();
  const [ask, setAsk] = useState(false);
  const c = useChange();
  if (!canWrite || !/^[A-Z]{2}$/.test(cc)) return null;
  const name = countryName(cc);
  return (
    <div class="mini-actions">
      {!ask && !c.s.note && (
        <button type="button" class="btn-mini" onClick={() => setAsk(true)}>Block for everyone</button>
      )}
      {ask && (
        <div class="confirm" role="group" aria-label={`Block ${name}`}>
          <span class="small">Block {name} for all devices? The country is told by the address: {CDN_NOTE}.</span>
          <span class="row-wrap">
            <button
              type="button"
              class="btn-mini btn-mini-danger"
              disabled={c.s.busy}
              onClick={() => {
                setAsk(false);
                void c.run(() => createRule({ what: "country", who: "all", targets: [cc] }));
              }}
            >
              Yes, block
            </button>
            <button type="button" class="btn-mini" onClick={() => setAsk(false)}>Cancel</button>
          </span>
        </div>
      )}
      <Outcome s={c.s} undo={() => void c.undo()} />
    </div>
  );
}

/** An app row: block it on this device or for everyone, always; a schedule is set on the Rules tab. */
export function BlockApp({ appId, name, mac }: { appId: string; name: string; mac: string }) {
  const { canWrite } = useSession();
  const c = useChange();
  if (!canWrite || !appId || appId === "other" || appId === "unknown" || name.includes(" · ")) return null;
  const go = (who: "device" | "all") =>
    void c.run(() => createRule(who === "device" ? { what: "app", who, targets: [appId], members: [mac.toLowerCase()] } : { what: "app", who, targets: [appId] }));
  return (
    <div class="mini-actions">
      {!c.s.note && (
        <Fragment>
          <button type="button" class="btn-mini" disabled={c.s.busy} onClick={() => go("device")}>Block on this device</button>
          <button type="button" class="btn-mini" disabled={c.s.busy} onClick={() => go("all")}>Block for everyone</button>
        </Fragment>
      )}
      <Outcome s={c.s} undo={() => void c.undo()} />
    </div>
  );
}

/** The rules, polled; `refresh` asks again at once after a change. */
export function useRules(): { rules: Rule[] | undefined; refresh: () => void } {
  const [n, setN] = useState(0);
  const p = usePoll((s) => getJSON<Rule[]>("/api/rules", s), 10_000, [n]);
  return { rules: p.data, refresh: () => setN((x) => x + 1) };
}

/** The device card's buttons: Pause (1 h, until 07:00, no end) and 30 min more. */
export function DeviceActions({ d }: { d: Device }) {
  const { canWrite } = useSession();
  const { rules, refresh } = useRules();
  const [open, setOpen] = useState(false);
  const pause = useChange(refresh);
  const more = useChange(refresh);
  const lift = useChange(refresh);
  const now = Date.now();
  const mac = d.mac.toLowerCase();
  const paused = activePause(rules, mac, now);
  const blocking = (rules ?? []).some((r) => r.what !== "country" && r.enabled && r.activeNow && appliesTo(r, mac));

  const start = (k: PauseKind) => {
    setOpen(false);
    void pause.run(async () => {
      const r = await sendJSON<Rule & Created>("POST", "/api/rules", pauseBody(mac, k, new Date()));
      const id = r?.id ?? "";
      return {
        note: k === "forever" ? "Device paused with no end." : "Device paused.",
        undo: id ? () => sendJSON("DELETE", `/api/rules/${encodeURIComponent(id)}`) : undefined,
      };
    });
  };
  const thirty = () =>
    void more.run(async () => {
      const r = await sendJSON<{ id: string; until: number }>("POST", "/api/allow", { rule: "", mac, minutes: 30 });
      return {
        note: `Allowed 30 min more, until ${time(new Date(r.until * 1000).toISOString())}.`,
        undo: r.id ? () => sendJSON("DELETE", `/api/allow/${encodeURIComponent(r.id)}`) : undefined,
      };
    });

  return (
    <div class="dev-actions">
      {paused && (
        <span class="act-note" role="status">
          Paused {paused.until ? `until ${untilText(paused.until)}` : "with no end"}.
          {canWrite && (
            <Fragment> <button type="button" class="link-btn" disabled={lift.s.busy} onClick={() => void lift.run(async () => { await sendJSON("DELETE", `/api/rules/${encodeURIComponent(paused.id)}`); return { note: "Pause lifted." }; })}>Lift pause</button></Fragment>
          )}
        </span>
      )}
      {canWrite && (
        <div class="actions">
          {paused ? null : !open ? (
            <button type="button" class="btn" disabled={pause.s.busy} aria-expanded="false" onClick={() => setOpen(true)}>Pause</button>
          ) : (
            <div class="pills" role="group" aria-label="Pause">
              {([["1h", "1 h"], ["07:00", "until 07:00"], ["forever", "no end"]] as [PauseKind, string][]).map(([k, l]) => (
                <button key={k} type="button" class="pill" onClick={() => start(k)}>{l}</button>
              ))}
              <button type="button" class="pill" onClick={() => setOpen(false)}>Cancel</button>
            </div>
          )}
          {blocking && (
            <button type="button" class="btn" disabled={more.s.busy} onClick={thirty}>30 min more</button>
          )}
        </div>
      )}
      <Outcome s={paused ? { ...pause.s, note: "", undo: null } : pause.s} undo={() => void pause.undo()} />
      <Outcome s={more.s} undo={() => void more.undo()} />
      <Outcome s={lift.s} undo={() => void lift.undo()} />
    </div>
  );
}

/** A live connection's Cut: the tuple names the connection, the router checks it is the device's own. */
export function CutButton({ mac, c }: { mac: string; c: LiveConn }) {
  const { canWrite } = useSession();
  const x = useChange();
  if (!canWrite || !c.local || !c.remote) return null;
  if (x.s.note) return <span class="cut"><span class="act-note" role="status">{x.s.note}</span></span>;
  return (
    <span class="cut">
      <button
        type="button"
        class="btn-mini"
        disabled={x.s.busy}
        onClick={() => void x.run(async () => {
          const a = await sendJSON<CutAnswer>("POST", `/api/devices/${enc(mac)}/conns/cut`, { local: c.local, remote: c.remote, proto: c.proto });
          return { note: cutNote(a) };
        })}
      >
        Cut
      </button>
      {x.s.err && <span class="act-err" role="alert">{x.s.err}</span>}
    </span>
  );
}

/** Isolation and quarantine on the device card: a held device can be approved,
 * any device isolated, and the card says how far the isolation reaches. */
export function DeviceSecurity({ d }: { d: Device }) {
  const { canWrite } = useSession();
  const x = useChange();
  const mac = d.mac.toLowerCase();
  const put = (body: Record<string, unknown>, note: string) =>
    void x.run(async () => {
      await sendJSON("PUT", `/api/devices/${enc(mac)}`, body);
      return { note };
    });
  return (
    <div class="dev-actions">
      {d.quarantined && (
        <p class="banner" role="status">
          New device in quarantine: only the internet, no access to the house.
          {d.likelySame && <span class="soft"> It may be a known device with a new address: the name it gives itself says so.</span>}
          {canWrite && (
            <Fragment> <button type="button" class="link-btn" disabled={x.s.busy} onClick={() => put({ approved: true }, "Approved: the device can now reach the house.")}>Approve</button></Fragment>
          )}
        </p>
      )}
      <label class="check">
        <input type="checkbox" checked={!!d.isolate} disabled={!canWrite || x.s.busy}
          onChange={() => put({ isolate: !d.isolate }, d.isolate ? "Isolation lifted." : "Device isolated.")} />
        <span>Isolate from the rest of the house</span>
        <span class="small muted"> only the internet and the router's DNS and DHCP</span>
      </label>
      {(d.isolate || d.quarantined) && d.isolation && <span class="small soft">{d.isolation.text}</span>}
      <Outcome s={x.s} undo={() => void x.undo()} />
    </div>
  );
}
