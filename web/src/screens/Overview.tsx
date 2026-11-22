// Overview: the state of the line, the house's traffic over the period, and who,
// what and where it went to.

import { ClientLink } from "./entity";
import { h, Fragment } from "preact";
import { useState } from "preact/hooks";
import type { Overview as OverviewData, Range } from "../types";
import { getJSON, usePoll } from "../api";
import { stepOf } from "../query";
import { splitSeries, timeTicks } from "../geometry";
import { StackedArea, Legend, Bars, type Layer } from "../charts";
import { Header, Pills, Card, CC, Status, Loading, Empty, LATER, AppIcon } from "../ui";
import { blockedTile, type Rule } from "../rules";
import { BlockCountry } from "./controls";
import { bytes, ratePair, duration, num, share, plural, appName, category, countryName, newDevices, clientName } from "../format";
import { alertText, alertsTile, telegramText, type AlertsState } from "../security";
import { LAYER_COLORS, OTHER_COLOR, UP_COLOR, countryList, lineQuality } from "../view";

type SplitKind = "mac" | "app" | "none";

export function Overview({ range, onRange }: { range: Range; onRange: (r: Range) => void }) {
  const [split, setSplit] = useState<SplitKind>("mac");
  const polled = usePoll((s) => getJSON<OverviewData>(`/api/overview?range=${range}&split=${split}`, s), 10_000, [range, split]);
  const d = polled.data;
  // The Blocked tile reads the rules' own counters; without them (a read
  // that failed) it says there is no data rather than 0.
  const rules = usePoll((s) => getJSON<Rule[]>("/api/rules", s), 10_000, []);
  const alerts = usePoll((s) => getJSON<AlertsState>("/api/alerts", s), 30_000, []);

  return (
    <div class="page">
      <Header section="overview" range={range} onRange={onRange} />
      <main class="main">
        <Status polled={polled} />
        {!d ? (
          <Loading />
        ) : (
          <div class="stack">
            <Tiles d={d} rules={rules.data} alerts={alerts.data} />
            <Traffic d={d} range={range} split={split} onSplit={setSplit} />
            <div class="grid-3">
              <TopClients d={d} />
              <TopApps d={d} />
              <Countries d={d} />
            </div>
            <div class="grid-3">
              <Security st={alerts.data} />
              <Now d={d} />
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

function Tiles({ d, rules, alerts }: { d: OverviewData; rules?: Rule[]; alerts?: AlertsState }) {
  const w = d.wan;
  const r = ratePair(w.rxRate, w.txRate);
  const addr = w.ipv4 || w.ipv6 || w.device || "no address";
  const q = lineQuality(w);
  const blocked = blockedTile(rules);
  const al = alertsTile(alerts, Date.now());
  const tiles: { label: string; value: string; sub: string; bad?: boolean; later?: boolean; href?: string }[] = [
    { label: "WAN", value: w.up ? "Online" : "No connection", sub: `${addr} · uptime ${duration(w.uptimeSec)}`, bad: !w.up },
    { label: "Speed now", value: `↓ ${r.down} · ↑ ${r.up}`, sub: r.unit + (w.device ? ` · ${w.device}` : "") },
    { label: "Line quality", value: q.value, sub: q.sub, bad: q.bad },
    { label: "Clients", value: `${num(d.clients.online)} online`, sub: newDevices(d.clients.new24h) },
    { label: "Alerts", value: al.value, sub: al.sub, href: "#/security" },
    { label: "Blocked", value: blocked.value, sub: blocked.sub, href: "#/rules" },
  ];
  return (
    <section aria-label="Network state" class="tiles">
      {tiles.map((t) => {
        const inner = (
          <Fragment>
            <span class="tile-label">{t.label}</span>
            <span class={t.bad ? "tile-value bad" : "tile-value"}>{t.value}</span>
            <span class="tile-sub">{t.sub}</span>
          </Fragment>
        );
        return t.href ? (
          <a key={t.label} class="tile tile-link" href={t.href}>{inner}</a>
        ) : (
          <div key={t.label} class={t.later ? "tile later" : "tile"} title={t.later ? LATER : undefined} aria-disabled={t.later ? "true" : undefined}>
            {inner}
          </div>
        );
      })}
    </section>
  );
}

function Traffic({ d, range, split, onSplit }: { d: OverviewData; range: Range; split: SplitKind; onSplit: (s: SplitKind) => void }) {
  // The rollup's catch-all key joins the folded rest: one "others" layer, not two.
  const s = splitSeries(d.series, stepOf(range), split === "none" ? 1 : LAYER_COLORS.length, ["other"]);
  const names = new Map<string, string>();
  for (const c of d.topClients) names.set(c.mac, clientName(c));
  for (const a of d.topApps) names.set(a.id, appName(a.name));
  const label = (k: string) => (split === "none" ? "down" : names.get(k) ?? (split === "app" ? appName(k) : k));

  // Bottom of the stack first: "the rest", then the smaller top keys, the largest on top.
  const layers: Layer[] = [];
  if (s.other) layers.push({ label: otherLabel(split, d), color: OTHER_COLOR, values: s.otherDown });
  for (let i = s.keys.length - 1; i >= 0; i--) {
    layers.push({ label: label(s.keys[i]), color: LAYER_COLORS[i], values: s.down[s.keys[i]] });
  }
  const legend = [...layers].reverse().map((l) => ({ label: l.label, color: l.color }));
  legend.push({ label: split === "none" ? "up" : "up (all)", color: UP_COLOR });

  return (
    <Card label="WAN traffic">
      <div class="card-head">
        <div class="card-titles">
          <h2>WAN traffic</h2>
          <span class="muted small">above the axis down, below it up, in bit/s</span>
        </div>
        <Pills
          label="Chart split"
          inset
          value={split}
          onPick={(v) => onSplit(v as SplitKind)}
          items={[{ id: "mac", label: "by client" }, { id: "app", label: "by app" }, { id: "none", label: "total" }]}
        />
      </div>
      {d.series.length === 0 ? (
        <Empty>No traffic in this period yet.</Empty>
      ) : (
        <StackedArea layers={layers} up={s.up} viewH={260} axis={170} downH={160} upH={80} label="WAN traffic chart for the period" />
      )}
      <Legend items={legend} ticks={timeTicks(s.t, range)} />
    </Card>
  );
}

function otherLabel(split: SplitKind, d: OverviewData): string {
  if (split === "app") return "other apps";
  const rest = Math.max(0, d.clients.online - LAYER_COLORS.length);
  return rest > 0 ? `${plural(rest, "other client", "other clients")}` : "other clients";
}

function TopClients({ d }: { d: OverviewData }) {
  let house = 0;
  for (const p of d.series) {
    for (const v of Object.values(p.down)) house += v;
    for (const v of Object.values(p.up)) house += v;
  }
  if (house === 0) house = d.topClients.reduce((a, c) => a + c.down + c.up, 0);
  return (
    <Card label="Top clients" title="Top clients" aside={<a href="#/clients" class="small">all clients</a>}>
      {d.topClients.length === 0 && <Empty>No clients in this period.</Empty>}
      {d.topClients.map((c) => (
        <div key={c.mac} class="row-item">
          <div class="row-between">
            <span class="strong">{c.other ? (c.mac === "other" ? "other devices" : clientName(c)) : <ClientLink mac={c.mac} name={clientName(c)} />}</span>
            <span class="soft small">↓ {bytes(c.down)} · ↑ {bytes(c.up)}</span>
          </div>
          <Bars parts={[{ pct: house ? (100 * c.down) / house : 0, kind: "down" }, { pct: house ? (100 * c.up) / house : 0, kind: "up" }]} />
          {c.other ? (
            <div class="muted small">{c.mac === "other" ? "folded rows" : c.mac === "router" ? "the router's own traffic" : "device without a hardware address"}</div>
          ) : (
            <div class="muted small row-gap">
              <span>{c.topApp ? appName(c.topApp.name) : "app unknown"}</span>
              <span>·</span>
              <span>{countryList(c.countries) || "no countries"}</span>
            </div>
          )}
        </div>
      ))}
    </Card>
  );
}

function TopApps({ d }: { d: OverviewData }) {
  return (
    <Card label="Top apps" title="Top apps">
      {d.topApps.length === 0 && <Empty>No apps in this period.</Empty>}
      {d.topApps.map((a) => {
        const name = a.other ? "other apps" : appName(a.name);
        return (
          <div key={a.id} class="app-row">
            <AppIcon id={a.other ? "other" : a.id} name={name} size={34} />
            <div class="col-tight">
              <span class="strong ellipsis">{name}</span>
              <span class="muted small">
                {a.other
                  ? "folded rows"
                  : `${name.includes(" · ") ? "by organisation" : category(a.category) || "no category"} · ${plural(a.clients, "client", "clients")}`}
              </span>
            </div>
            <span class="soft small right">
              ↓ {bytes(a.down)}
              <br />↑ {bytes(a.up)}
            </span>
          </div>
        );
      })}
    </Card>
  );
}

function Countries({ d }: { d: OverviewData }) {
  return (
    <Card label="Where traffic goes" title="Where traffic goes">
      {d.countries.length === 0 && <Empty>No countries in this period.</Empty>}
      {d.countries.map((k) => (
        <div key={k.cc} class="country-row">
          <CC cc={k.cc} />
          <div class="col-tight">
            <span>
              {k.other || k.cc === "--" ? "unknown or others" : countryName(k.cc)}
              {k.org && <span class="muted small"> · {k.org}</span>}
            </span>
            <Bars thin parts={[{ pct: k.share * 100, kind: "down" }]} />
            {!k.other && k.cc !== "--" && k.cc !== "LAN" && <BlockCountry cc={k.cc} />}
          </div>
          <span class="soft small right">{share(k.share)}</span>
        </div>
      ))}
    </Card>
  );
}

// Security: the newest alerts and how they reach the owner; the threat feeds
// of the mockup are still to come and say so.
function Security({ st }: { st?: AlertsState }) {
  return (
    <section aria-label="Security" class="card">
      <div class="card-head">
        <h2>Security</h2>
        <a class="small" href="#/security">all</a>
      </div>
      {st && <span class="small soft">{telegramText(st)}</span>}
      {st && st.alerts.length === 0 && <span class="small muted">No alerts yet.</span>}
      {(st?.alerts ?? []).slice(0, 4).map((a) => (
        <div key={a.id} class="alert-row">
          <span class="dot dot-on" aria-hidden="true" />
          <span class="small-13 break">{alertText(a, (m) => m)}</span>
        </div>
      ))}
      {[
        ["Domain and IP reputation", "blocking by threat feeds, with the reason and the evidence"],
      ].map(([title, detail]) => (
        <div key={title} class="alert-row">
          <span class="dot dot-off" aria-hidden="true" />
          <div class="col-tight">
            <span class="strong">{title}</span>
            <span class="soft small">{detail}</span>
          </div>
          <span class="muted small nowrap">later</span>
        </div>
      ))}
    </section>
  );
}

function Now({ d }: { d: OverviewData }) {
  return (
    <section aria-label="Live" class="card">
      <h2>Now</h2>
      <div class="now">
        <a class="now-item" href="#/flows">
          <span class="muted small">Active flows</span>
          <span class="tile-value">{num(d.liveFlows)}</span>
        </a>
        <div class="now-item">
          <span class="muted small">New devices</span>
          <span class="tile-value">{d.clients.new24h === null ? "-" : num(d.clients.new24h)}</span>
          {d.clients.new24h === null && <span class="muted small">shown after a day</span>}
        </div>
        <div class="now-item later" title={LATER} aria-disabled="true">
          <span class="muted small">New countries</span>
          <span class="tile-value">-</span>
          <span class="muted small">coming later</span>
        </div>
      </div>
    </section>
  );
}
