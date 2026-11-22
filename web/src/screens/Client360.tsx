// A client's full view: who it is, where it is connected, a map of where its
// connections go, what it does now, its history, the rules and limits on it,
// its security, and what can be done to it. Each section has its own empty
// and error state, so one router read failing never blanks the page.

import { parentsOf } from "../entity";
import { ownerOf } from "../people";
import { ChipChain, EntityLink } from "./entity";
import { h, Fragment, type ComponentChildren } from "preact";
import { useContext, useRef, useState } from "preact/hooks";
import type { App, AppUsage, Destinations, Device, FlowsPage, Group, Person, Live, NetworkView, Range, SeriesPoint } from "../types";
import { getJSON, mac as enc, usePoll, type Polled } from "../api";
import { RANGES, windowLabel, type Win } from "../query";
import { Header, Pills, Card, Status, Loading, Empty, IconContext, usePeriod } from "../ui";
import { setSpan } from "../span";
import { Sankey } from "../charts";
import { date, time, groupName } from "../format";
import { alertText, type AlertsState } from "../security";
import { connMap } from "../connmap";
import { rulesOn, whereConnected } from "../client360";
import { allowText, liveAllows, manifestName, stateOf, usageLabel, usageText, type Usage } from "../rules";
import { deviceName } from "./Clients";
import { RuleTitle } from "./Rules";
import { Apps, Traffic, Where } from "./Client";
import { LiveApps, LiveConns, LivePlaces, LiveTiles, LiveTraffic } from "./ClientLive";
import { DeviceActions, DeviceEdit, DeviceSecurity, useRules } from "./controls";

type SplitKind = "app" | "none";

/** A section that says when its read failed or has nothing, instead of going blank. */
function Section<T>({ polled, empty, children }: { polled: Polled<T>; empty?: (d: T) => string; children: (d: T) => ComponentChildren }) {
  if (polled.error && polled.data === undefined) return <p class="bad-text small">The router did not answer this part: {polled.error}.</p>;
  if (polled.data === undefined) return <Loading />;
  const e = empty?.(polled.data);
  return e ? <Empty>{e}</Empty> : <>{children(polled.data)}</>;
}

export function Client360({ mac, from = "", range, onRange, win, onWin }: { mac: string; from?: string; range: Range; onRange: (r: Range) => void; win: Win; onWin: (w: Win) => void }) {
  const base = `/api/devices/${enc(mac)}`;
  const dev = usePoll((s) => getJSON<Device>(`${base}?window=${win}`, s), 10_000, [mac, win]);
  const d = dev.data;
  const name = d ? deviceName(d) : mac;
  const notFound = dev.status === 404 && !d;
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  const people = usePoll((s) => getJSON<Person[]>("/api/people", s), 60_000, []).data;
  return (
    <div class="page">
      <Header section="clients" range={range} onRange={onRange} win={win} onWin={onWin} />
      <main class="main">
        <ChipChain from={from} base={[{ label: "Clients", href: "#/clients" }]} last={name} parents={parentsOf({ kind: "client", mac }, d, { group: (g) => groupName(g, groups), person: (m) => ownerOf(people, m) })} />
        {notFound ? <Empty>The router knows no device with MAC {mac.toUpperCase()}.</Empty> : <Status polled={dev} />}
        {!d ? (
          notFound ? null : <Loading />
        ) : (
          <div class="stack">
            <Identity d={d} person={ownerOf(people, d.mac)} />
            <div class="grid-2">
              <Connected d={d} />
              <SecuritySection d={d} />
            </div>
            <ConnectionMap mac={mac} name={name} win={win} />
            <Now mac={mac} win={win} avg={d.recent} />
            <History mac={mac} range={range} onRange={onRange} win={win} />
            <RulesSection d={d} />
          </div>
        )}
      </main>
    </div>
  );
}

function Identity({ d, person }: { d: Device; person?: { id: string; name: string } }) {
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  const rows: [string, ComponentChildren][] = [
    ["Vendor", d.vendor ? `${d.vendor} (by OUI)` : "-"],
    ["MAC", d.mac.toUpperCase() + (d.random ? " (random, made up by the device)" : "")],
    ["IPv4", (d.ipv4 ?? []).join(", ") || "-"],
    ["IPv6", (d.ipv6 ?? []).join(", ") || "-"],
    ["Hostname", d.hostname || "-"],
    ["Person", person ? <EntityLink e={{ kind: "person", id: person.id }}>{person.name}</EntityLink> : <a href="#/people">nobody yet</a>],
    ["Group", d.group ? <EntityLink e={{ kind: "group", id: d.group }}>{groupName(d.group, groups)}</EntityLink> : "no group"],
    ["First seen", date(d.first)],
    ["Last seen", d.online ? "online now" : `${date(d.last)} ${time(d.last)}`],
  ];
  return (
    <section aria-label="Identity" class="card">
      <div class="card-head">
        <h1>{deviceName(d)}</h1>
        <span class={d.online ? "state state-active" : "soft small"}>{d.online ? "online" : "offline"}</span>
      </div>
      <div class="identity-grid">
        <div class="kv-mini">
          {rows.map(([k, v]) => [<span key={k + "k"}>{k}</span>, <span key={k + "v"} class="break">{v}</span>])}
        </div>
        <div class="col-tight">
          <DeviceActions d={d} />
          <DeviceEdit d={d} />
        </div>
      </div>
    </section>
  );
}

function Connected({ d }: { d: Device }) {
  const net = usePoll((s) => getJSON<NetworkView>("/api/network", s), 30_000, []);
  return (
    <Card label="Where it is connected" title="Where it is connected" aside={<a class="small" href="#/network">Network</a>}>
      <span>{whereConnected(d, net.data)}</span>
      {net.error && !net.data && <span class="muted small">The router's wiring did not answer, so the port's speed and the channel are not shown.</span>}
    </Card>
  );
}

function SecuritySection({ d }: { d: Device }) {
  const alerts = usePoll((s) => getJSON<AlertsState>("/api/alerts", s), 60_000, [d.mac]);
  return (
    <Card label="Security" title="Security" aside={<a class="small" href="#/security">Security</a>}>
      <DeviceSecurity d={d} />
      <span class="muted small">Alerts about this device</span>
      <Section polled={alerts} empty={(a) => (a.alerts.some((x) => x.mac === d.mac.toLowerCase()) ? "" : "No alerts about this device.")}>
        {(a) =>
          a.alerts
            .filter((x) => x.mac === d.mac.toLowerCase())
            .slice(0, 5)
            .map((x) => <span key={x.id} class="small-13 break">{alertText(x, () => deviceName(d))}</span>)
        }
      </Section>
    </Card>
  );
}

function ConnectionMap({ mac, name, win }: { mac: string; name: string; win: Win }) {
  const flows = usePoll((s) => getJSON<FlowsPage>(`/api/flows?live=1&mac=${enc(mac)}&window=${win}&limit=500`, s), 10_000, [mac, win]);
  return (
    <Card label="Connection map" title="Connection map" aside={<span class="muted small">device → apps → domains → networks → countries, over the last {windowLabel(win)}; click a name to go there</span>}>
      <Section polled={flows} empty={(p) => (connMap(name, p.rows).nodes.length ? "" : `Nothing moved in the last ${windowLabel(win)}.`)}>
        {(p) => {
          const m = connMap(name, p.rows);
          return (
            <div class="map-scroll">
              <Sankey nodes={m.nodes} links={m.links} href={m.href} cols={5} width={1000} height={340} label={`Map: where ${name}'s connections went over the last ${windowLabel(win)}`} />
            </div>
          );
        }}
      </Section>
    </Card>
  );
}

function Now({ mac, win, avg }: { mac: string; win: Win; avg?: { down: number; up: number } }) {
  const base = `/api/devices/${enc(mac)}`;
  const live = usePoll((s) => getJSON<Live>(`${base}/live`, s), 2_000, [mac]);
  const recent = usePoll((s) => getJSON<AppUsage[]>(`${base}/apps?range=1h&window=${win}`, s), 10_000, [mac, win]);
  // Apps seen in this view keep their names on the chart after they stop.
  const seen = useRef(new Map<string, App>());
  for (const a of live.data?.apps ?? []) seen.current.set(a.app.id, a.app);
  for (const c of live.data?.conns ?? []) seen.current.set(c.app.id, c.app);
  return (
    <section aria-label="Now" class="col-tight">
      <div class="card-head">
        <h2>Now</h2>
        <a class="btn" href={`#/flows?q=${encodeURIComponent("mac:" + mac)}`}>Show these flows</a>
      </div>
      <Section polled={live}>
        {(l) => (
          <div class="stack">
            <LiveTiles l={l} recent={avg} win={win} />
            <LiveTraffic l={l} seen={seen.current} />
            <LiveApps apps={l.apps ?? []} mac={mac} recent={recent.data ?? []} win={win} />
            <div class="grid-2">
              <LiveConns mac={mac} conns={l.conns ?? []} flows={l.flows} />
              <LivePlaces places={l.places ?? []} />
            </div>
          </div>
        )}
      </Section>
    </section>
  );
}

function History({ mac, range, onRange, win }: { mac: string; range: Range; onRange: (r: Range) => void; win: Win }) {
  const [split, setSplit] = useState<SplitKind>("app");
  const base = `/api/devices/${enc(mac)}`;
  const per = usePeriod(range);
  const alerts = usePoll((s) => getJSON<AlertsState>("/api/alerts", s), 60_000, []).data;
  const marks = (alerts?.alerts ?? []).filter((a) => a.mac === mac.toLowerCase());
  const hist = usePoll(
    async (s) => {
      const [traffic, apps, dest] = await Promise.all([
        getJSON<SeriesPoint[]>(`${base}/traffic?${per.q}&split=${split}`, s),
        getJSON<AppUsage[]>(`${base}/apps?${per.q}&window=${win}`, s),
        getJSON<Destinations>(`${base}/destinations?${per.q}`, s),
      ]);
      return { traffic, apps, dest };
    },
    15_000,
    [mac, per.q, split, win],
  );
  return (
    <section aria-label="History" class="col-tight">
      <div class="card-head">
        <h2>History</h2>
        <Pills label="History period" inset value={per.span ? "" : range} onPick={(v) => { if (per.span) setSpan(null); onRange(v as Range); }} items={RANGES.map((r) => ({ id: r.id, label: r.label }))} />
      </div>
      <Section polled={hist}>
        {(got) => (
          <div class="stack">
            <Traffic d={got} step={per.step} range={per.range} span={!!per.span} split={split} onSplit={setSplit} alerts={marks} />
            <Apps apps={got.apps} mac={mac} win={win} />
            <Where dest={got.dest} />
          </div>
        )}
      </Section>
    </section>
  );
}

function RulesSection({ d }: { d: Device }) {
  const { rules } = useRules();
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  const usage = usePoll((s) => getJSON<Usage[]>(`/api/usage?mac=${enc(d.mac)}`, s), 60_000, [d.mac]);
  const appNames = manifestName(useContext(IconContext));
  const on = rulesOn(rules, d.mac, groups);
  const now = Date.now();
  return (
    <Card label="Rules and limits" title="Rules and limits" aside={<a class="small" href="#/rules">all rules</a>}>
      {!rules ? (
        <Loading />
      ) : on.length === 0 ? (
        <Empty>No rule reaches this device.</Empty>
      ) : (
        on.map(({ rule: r, from }) => (
          <div key={r.id} class="col-tight small-13 rule-on">
            <RuleTitle r={r} names={{ device: (m) => (m === d.mac ? deviceName(d) : m), app: appNames, group: (g) => groupName(g, groups) }} class="break" />
            <span class="muted small"><EntityLink e={{ kind: "rule", id: r.id }}>the rule</EntityLink> · from {from} · blocked {r.hits} {r.hits === 1 ? "time" : "times"}</span>
            <span class={`state state-${stateOf(r, now).kind}`}><span class="dot" aria-hidden="true" />{stateOf(r, now).text}</span>
            {liveAllows(r, now, d.mac).map((a) => <span key={a.id} class="soft">{allowText(a)}</span>)}
          </div>
        ))
      )}
      <Section polled={usage} empty={(u) => (u.length ? "" : "No time limit on this device.")}>
        {(u) =>
          u.map((x) => (
            <div key={x.rule} class="row-wrap small-13">
              <span class="break"><EntityLink e={{ kind: "rule", id: x.rule ?? "" }}>{usageLabel(x, appNames)}</EntityLink>:</span>
              <span class={x.spentUntil ? "state state-active" : "soft"}>{usageText(x)}</span>
            </div>
          ))
        }
      </Section>
    </Card>
  );
}

