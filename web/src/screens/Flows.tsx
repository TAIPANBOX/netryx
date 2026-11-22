// Flows: the live table and the journal, searchable, with one flow's details
// beside it.

import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Device, FlowRow, FlowsPage } from "../types";
import { getJSON, usePoll } from "../api";
import { flowsPath, parseFlowQuery, type FlowFilter } from "../query";
import { Header, Pills, Status, Loading, Empty, LATER, AppIcon } from "../ui";
import { flowKey, selectFlow } from "../view";
import { bytes, time, dateTime, duration, appName, category, countryName, dirArrow, nameSource, port, isZeroTime, num, clientName } from "../format";

const PRESETS = [
  { id: "", label: "All" },
  { id: "out", label: "Outgoing" },
  { id: "in", label: "Incoming" },
  { id: "local", label: "Local" },
];
const LIMIT = 200;

function seconds(f: FlowRow, now: number): number {
  const start = Date.parse(f.start);
  const end = isZeroTime(f.end) ? now : Date.parse(f.end);
  return Number.isNaN(start) ? 0 : Math.max(0, (end - start) / 1000);
}

function target(f: FlowRow): string {
  if (f.name) return f.name;
  if (f.nameSrc === "ech") return "name hidden (ECH)";
  return f.remote.replace(/:\d+$/, "").replace(/^\[(.*)\]$/, "$1");
}

const DIR_TEXT: Record<string, string> = { out: "outgoing (from the house)", in: "incoming (from the internet)", local: "local" };

export function Flows({ initial }: { initial: string }) {
  const [text, setText] = useState(initial);
  const [applied, setApplied] = useState(initial);
  const [preset, setPreset] = useState("");
  const [live, setLive] = useState(true);
  const [selKey, setSelKey] = useState("");
  const lastSeen = useRef<FlowRow | null>(null);
  const lastSeenAt = useRef(0);
  const [more, setMore] = useState<{ rows: FlowRow[]; next: string } | null>(null);
  const [moreState, setMoreState] = useState<{ busy: boolean; error: string }>({ busy: false, error: "" });
  // A ref, not state: two clicks in one frame both see the state from before
  // the first, and would ask for the same page twice.
  const moreBusy = useRef(false);

  // A search is applied when typing pauses, not on every key.
  useEffect(() => {
    const t = setTimeout(() => setApplied(text), 400);
    return () => clearTimeout(t);
  }, [text]);

  const devices = usePoll((s) => getJSON<Device[]>("/api/devices", s), 60_000, []);
  const names = (devices.data ?? []).map((d) => ({ mac: d.mac, name: d.label || d.name || d.hostname }));
  const filter: FlowFilter = parseFlowQuery(applied, names);
  if (preset) filter.dir = preset;
  const key = JSON.stringify(filter);
  const keyNow = useRef(key);
  keyNow.current = key + (live ? "|live" : "");

  const polled = usePoll((s) => getJSON<FlowsPage>(flowsPath(filter, { live, limit: LIMIT }), s), live ? 5_000 : 0, [key, live]);
  useEffect(() => {
    setMore(null);
    setSelKey("");
    lastSeen.current = null;
    setMoreState({ busy: false, error: "" });
  }, [key, live]);

  const rows = [...(polled.data?.rows ?? []), ...(more?.rows ?? [])];
  const next = more ? more.next : polled.data?.next ?? "";
  const now = polled.at ?? Date.now();
  // The panel follows a flow by its key, not its row number: a live refresh
  // reorders rows, and a flow that ended stays shown with a note.
  const picked = selectFlow(rows, selKey, lastSeen.current);
  if (picked.row && !picked.ended) {
    lastSeen.current = picked.row;
    lastSeenAt.current = now;
  }
  const shownKey = picked.row ? flowKey(picked.row) : "";

  const loadMore = async () => {
    if (moreBusy.current || !next) return;
    moreBusy.current = true;
    const asked = keyNow.current;
    setMoreState({ busy: true, error: "" });
    try {
      const page = await getJSON<FlowsPage>(flowsPath(filter, { live: false, limit: LIMIT, cursor: next }));
      // A page for a search that has since changed belongs to nobody.
      if (keyNow.current !== asked) return;
      setMore((m) => ({ rows: [...(m?.rows ?? []), ...page.rows], next: page.next }));
      setMoreState({ busy: false, error: "" });
    } catch (e) {
      if (keyNow.current !== asked) return;
      setMoreState({ busy: false, error: e instanceof Error ? e.message : String(e) });
    } finally {
      moreBusy.current = false;
    }
  };

  return (
    <div class="page">
      <Header section="flows" />
      <main class="main">
        <div class="toolbar">
          <h1>Flows</h1>
          <Pills label="Presets" items={PRESETS} value={preset} onPick={setPreset} />
          <span class="muted small push">
            {live ? "live connection table" : "journal"} · showing {num(rows.length)}
          </span>
        </div>
        <div class="toolbar">
          <div class="field grow">
            <label for="q" class="muted small">Search and filters</label>
            <input
              id="q"
              type="search"
              class="input"
              value={text}
              placeholder={'client:"Yard camera" dir:out app:netflix cc:DE or any text'}
              onInput={(e) => setText((e.target as HTMLInputElement).value)}
            />
          </div>
          {["Columns", "Save view", "Export CSV"].map((b) => (
            <button key={b} type="button" class="btn" disabled title={LATER}>{b}</button>
          ))}
          <button type="button" class={live ? "pill active pill-lg" : "pill pill-lg pill-outline"} aria-pressed={live} onClick={() => setLive(!live)}>
            {live ? "Live: 5 s" : "History"}
          </button>
        </div>
        <Status polled={polled} />
        <div class="split">
          <section aria-label="Flows table" class="card card-flush flows-card">
            <div class="scroll-x">
              <div class="flows-table">
                <div class="flows-grid flows-head">
                  <span>End</span>
                  <span>Client</span>
                  <span>Dir.</span>
                  <span>Target and name source</span>
                  <span>App</span>
                  <span>Country · ASN</span>
                  <span>Port</span>
                  <span>↓ down</span>
                  <span>↑ up</span>
                  <span>Dur.</span>
                </div>
                {!polled.data && !polled.error && <Loading />}
                {polled.data && rows.length === 0 && <Empty>No flows match this search.</Empty>}
                {rows.map((f, i) => (
                  <button
                    key={i}
                    type="button"
                    class={flowKey(f) === shownKey ? "flows-grid flows-row active" : "flows-grid flows-row"}
                    onClick={() => {
                      setSelKey(flowKey(f));
                      lastSeen.current = f;
                    }}
                  >
                    <span class="muted">{isZeroTime(f.end) ? "now" : time(f.end)}</span>
                    <span class="strong ellipsis">{clientName({ mac: f.mac, name: f.client })}</span>
                    <span>{dirArrow(f.dir)}</span>
                    <span class="ellipsis">
                      {target(f)}
                      {f.nameSrc && <span class="muted small"> · {nameSource(f.nameSrc)}</span>}
                    </span>
                    <span class="app-cell">
                      <AppIcon id={f.app.id} name={appName(f.app.name)} size={20} />
                      <span class="ellipsis">{appName(f.app.name)}</span>
                    </span>
                    <span class="ellipsis">
                      <b>{f.cc || "--"}</b> <span class="muted small">{f.asn ? `AS${f.asn}` : ""}</span>
                    </span>
                    <span class="soft">{port(f.remote, f.proto)}</span>
                    <span>{bytes(f.down)}</span>
                    <span>{bytes(f.up)}</span>
                    <span class="soft">{duration(seconds(f, now))}</span>
                  </button>
                ))}
              </div>
            </div>
            {!live && next && (
              <div class="more">
                <button type="button" class="btn" disabled={moreState.busy} onClick={() => void loadMore()}>
                  {moreState.busy ? "Loading…" : "Show more"}
                </button>
                {moreState.error && <span class="bad-text small"> Failed: {moreState.error}</span>}
              </div>
            )}
          </section>
          <aside aria-label="Flow details" class="card detail">
            <h2>Flow details</h2>
            {picked.ended && <p class="note small">The flow has ended: showing the last known data.</p>}
            {picked.row ? <Detail f={picked.row} now={picked.ended ? lastSeenAt.current : now} ended={picked.ended} /> : <span class="muted small">Pick a row in the table.</span>}
            <div class="detail-actions">
              <span class="muted small">What to block or allow</span>
              <div class="row-wrap">
                <button type="button" class="btn btn-danger" disabled title={LATER}>Block</button>
                <button type="button" class="btn" disabled title={LATER}>Allow</button>
                <button type="button" class="btn btn-ghost" disabled title={LATER}>Wrong app or country</button>
              </div>
            </div>
          </aside>
        </div>
      </main>
    </div>
  );
}

function Detail({ f, now, ended }: { f: FlowRow; now: number; ended: boolean }) {
  const rows: [string, string][] = [
    ["Client", f.mac === "router" ? "Router" : f.client === f.mac ? f.mac.toUpperCase() : `${f.client} (${f.mac.toUpperCase()})`],
    ["Time", `${dateTime(f.start)} - ${!isZeroTime(f.end) ? dateTime(f.end) : ended ? "ended" : "ongoing"}, duration ${duration(seconds(f, now))}`],
    ["Direction", DIR_TEXT[f.dir] ?? f.dir],
    ["Target", target(f) + (f.nameSrc ? ` (${nameSource(f.nameSrc)})` : "")],
    ["Remote address", f.remote],
    ["Local address", f.local],
    ["Port", port(f.remote, f.proto)],
    ["App", appName(f.app.name) + (f.app.category ? ` · ${category(f.app.category)}` : "")],
    ["Country", f.cc ? `${f.cc} · ${countryName(f.cc)}` : "--"],
    ["ASN", f.asn ? `AS${f.asn}${f.org ? " · " + f.org : ""}` : f.org || "-"],
    ["Bytes", `↓ ${bytes(f.down)} · ↑ ${bytes(f.up)}`],
  ];
  if (f.evicted) rows.push(["Accounting", "the flow was pushed out of a full table; bytes moved outside it are counted as lost"]);
  return (
    <div class="kv">
      {rows.map(([k, v]) => [
        <span key={k + "k"} class="muted">{k}</span>,
        <span key={k + "v"} class="break">{v}</span>,
      ])}
    </div>
  );
}
