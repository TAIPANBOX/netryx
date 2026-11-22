// Client card: one device. It opens in "Now" (ClientLive: what the device
// is doing right now); a period shows its
// traffic over the period, each app it used with that app's domains and
// countries, the Sankey, where it went and its latest flows.

import { h, type ComponentChildren } from "preact";
import { useContext, useState } from "preact/hooks";
import { alertText, type AlertsState } from "../security";
import type { AppUsage, Destinations, Device, FlowsPage, Group, Range, SeriesPoint } from "../types";
import { getJSON, mac as enc, usePoll } from "../api";
import { stepOf, RANGES } from "../query";
import { splitSeries, timeTicks } from "../geometry";
import { StackedArea, Legend, Sankey, type Layer } from "../charts";
import { Header, Pills, Card, CC, Status, Loading, Empty, LATER, AppIcon, IconContext } from "../ui";
import { bytes, rate, share, num, time, date, appName, category, countryName, nameSource, plural, groupName } from "../format";
import { LAYER_COLORS, OTHER_COLOR, UP_COLOR, isRealCountry, sankeyOf } from "../view";
import { deviceName, linkText } from "./Clients";
import { ClientLive } from "./ClientLive";
import { BlockApp, DeviceActions, DeviceSecurity, useRules } from "./controls";
import { allowText, appliesTo, describeRule, liveAllows, manifestName, stateOf, usageLabel, usageText, type Usage } from "../rules";

interface ClientData {
  device: Device;
  traffic: SeriesPoint[];
  apps: AppUsage[];
  dest: Destinations;
  flows: FlowsPage;
}

type SplitKind = "app" | "none";
type Mode = "live" | Range;

/**
 * The card always opens in "Now". Picking a period shows the period
 * sections and also becomes the period the other screens use.
 */
export function Client({ mac, onRange }: { mac: string; range: Range; onRange: (r: Range) => void }) {
  const [mode, setMode] = useState<Mode>("live");
  const pick = (id: string) => {
    if (id === "live") {
      setMode("live");
      return;
    }
    setMode(id as Range);
    onRange(id as Range);
  };
  const modes = (
    <Pills label="Mode" value={mode} onPick={pick} items={[{ id: "live", label: "Now" }, ...RANGES.map((r) => ({ id: r.id, label: r.label }))]} />
  );
  return mode === "live" ? <ClientLive mac={mac} modes={modes} /> : <ClientPeriod mac={mac} range={mode} modes={modes} />;
}

function ClientPeriod({ mac, range, modes }: { mac: string; range: Range; modes: ComponentChildren }) {
  const [split, setSplit] = useState<SplitKind>("app");
  const base = `/api/devices/${enc(mac)}`;
  const polled = usePoll(
    async (s): Promise<ClientData> => {
      const [device, traffic, apps, dest, flows] = await Promise.all([
        getJSON<Device>(base, s),
        getJSON<SeriesPoint[]>(`${base}/traffic?range=${range}&split=${split}`, s),
        getJSON<AppUsage[]>(`${base}/apps?range=${range}`, s),
        getJSON<Destinations>(`${base}/destinations?range=${range}`, s),
        getJSON<FlowsPage>(`/api/flows?mac=${enc(mac)}&limit=5`, s),
      ]);
      return { device, traffic, apps, dest, flows };
    },
    10_000,
    [mac, range, split],
  );
  const d = polled.data;
  const name = d ? deviceName(d.device) : mac;

  return (
    <div class="page">
      <Header section="clients" pills={modes} />
      <main class="main">
        <div class="crumbs">
          <a href="#/clients">Clients</a> / {name}
        </div>
        {polled.status === 404 && !d ? (
          <Empty>The router knows no device with MAC {mac.toUpperCase()}.</Empty>
        ) : (
          <Status polled={polled} />
        )}
        {!d ? (
          polled.status === 404 ? null : <Loading />
        ) : (
          <div class="stack">
            <DeviceCard d={d.device} />
            <Traffic d={d} range={range} split={split} onSplit={setSplit} />
            <Apps apps={d.apps} mac={mac} />
            <div class="grid-2">
              <Card label="Traffic flow" title="Client → apps → countries">
                <Sankey {...sankeyOf(name, d.apps)} label={`Diagram: where ${name} sends traffic, by app and country`} />
              </Card>
              <Where dest={d.dest} />
            </div>
            <div class="grid-2">
              <Flows mac={mac} flows={d.flows} />
              <DeviceRules d={d.device} />
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

function DeviceCard({ d }: { d: Device }) {
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  const meta = [
    d.vendor ? `${d.vendor} (by OUI)` : "",
    ...(d.ipv4 ?? []),
    ...(d.ipv6 ?? []),
    `MAC ${d.mac.toUpperCase()}`,
    linkText(d),
    d.group ? `group "${groupName(d.group, groups)}"` : "no group",
    `first seen ${date(d.first)} · ${d.online ? "online now" : `last seen ${date(d.last)} ${time(d.last)}`}`,
  ].filter(Boolean);
  return (
    <section aria-label="Device" class="card device-card">
      <div class="device-id">
        <div class="device-icon">
          <svg width="30" height="30" viewBox="0 0 24 24" fill="none" stroke="#9BD0FF" stroke-width="1.8" aria-hidden="true">
            <rect x="2" y="4" width="20" height="13" rx="2" />
            <path d="M8 21h8M12 17v4" />
          </svg>
        </div>
        <div class="col-tight">
          <h1>{deviceName(d)}</h1>
          <div class="meta">
            {meta.map((m) => (
              <span key={m}>{m}</span>
            ))}
            {d.random && <span class="tag">random MAC</span>}
          </div>
        </div>
      </div>
      <DeviceActions d={d} />
      <DeviceSecurity d={d} />
      <div class="actions">
        {["Speed limit", "Rename"].map((a) => (
          <button key={a} type="button" class="btn" disabled title={LATER}>{a}</button>
        ))}
      </div>
    </section>
  );
}

function Traffic({ d, range, split, onSplit }: { d: ClientData; range: Range; split: SplitKind; onSplit: (s: SplitKind) => void }) {
  const s = splitSeries(d.traffic, stepOf(range), split === "none" ? 1 : 3, ["other"]);
  const names = new Map(d.apps.map((a) => [a.app.id, appName(a.app.name)]));
  const layers: Layer[] = [];
  if (s.other) layers.push({ label: "others", color: OTHER_COLOR, values: s.otherDown });
  const colors = [LAYER_COLORS[0], LAYER_COLORS[2], LAYER_COLORS[1]];
  for (let i = s.keys.length - 1; i >= 0; i--) {
    const k = s.keys[i];
    layers.push({ label: split === "none" ? "down" : names.get(k) ?? appName(k), color: colors[i], values: s.down[k] });
  }
  const legend = [...layers].reverse().map((l) => ({ label: l.label, color: l.color }));
  legend.push({ label: "up", color: UP_COLOR });
  const period = RANGES.find((r) => r.id === range)?.label ?? range;
  const peak = s.peakDown.rate > 0 ? ` · peak ${rate(s.peakDown.rate)} at ${time(s.peakDown.at)}` : "";

  return (
    <Card label="Client traffic">
      <div class="card-head">
        <div class="card-titles">
          <h2>Client traffic</h2>
          <span class="muted small">
            ↓ {bytes(s.totalDown)} · ↑ {bytes(s.totalUp)} over {period}
            {peak}
          </span>
        </div>
        <Pills label="Split" inset value={split} onPick={(v) => onSplit(v as SplitKind)} items={[{ id: "app", label: "by app" }, { id: "none", label: "total" }]} />
      </div>
      {d.traffic.length === 0 ? (
        <Empty>No traffic in this period.</Empty>
      ) : (
        <StackedArea layers={layers} up={s.up} viewH={220} axis={160} downH={150} upH={55} label="Client traffic chart for the period" />
      )}
      <Legend items={legend} ticks={timeTicks(s.t, range)} />
    </Card>
  );
}

function Apps({ apps, mac }: { apps: AppUsage[]; mac: string }) {
  const [open, setOpen] = useState<string>("");
  return (
    <Card label="Client apps" title="Apps" aside={<span class="muted small">click a row for that app's domains and countries</span>}>
      {apps.length === 0 ? (
        <Empty>No apps in this period.</Empty>
      ) : (
        <div class="scroll-x">
          <div class="apps-table">
            <div class="apps-grid apps-head">
              <span>App</span>
              <span>Category</span>
              <span>↓ down</span>
              <span>↑ up</span>
              <span>Flows</span>
              <span>Share</span>
            </div>
            {apps.map((a) => {
              const isOpen = open === a.app.id;
              const name = a.other ? "other apps" : appName(a.app.name);
              return (
                <div key={a.app.id} class="apps-item">
                  <button type="button" class="apps-grid apps-row" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? "" : a.app.id)}>
                    <span class="strong app-cell">
                      <span class="caret" aria-hidden="true">{isOpen ? "▾" : "▸"}</span>
                      <AppIcon id={a.other ? "other" : a.app.id} name={name} size={24} />
                      <span class="ellipsis">{name}</span>
                    </span>
                    <span class="soft">{name.includes(" · ") ? "by organisation" : category(a.app.category) || "-"}</span>
                    <span>{bytes(a.down)}</span>
                    <span>{bytes(a.up)}</span>
                    <span>{num(a.flows)}</span>
                    <span>{share(a.share)}</span>
                  </button>
                  {isOpen && <AppDetail a={a} mac={mac} />}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </Card>
  );
}

function AppDetail({ a, mac }: { a: AppUsage; mac: string }) {
  const domains = a.domains ?? [];
  const places = a.places ?? [];
  return (
    <div class="app-detail">
      {!a.other && (
        <div class="app-detail-actions">
          <BlockApp appId={a.app.id} name={a.app.name} mac={mac} />
        </div>
      )}
      <div class="col-list">
        <span class="muted small">Domains (name source)</span>
        {domains.length === 0 && <span class="muted small">no names</span>}
        {domains.map((d) => (
          <div key={d.name} class="row-between small-13">
            <span class="ellipsis">
              {d.other ? "other domains" : d.name || "no name"}
              {d.src && !d.other && <span class="muted"> · {nameSource(d.src)}</span>}
            </span>
            <span class="soft nowrap">
              ↓ {bytes(d.down)} · ↑ {bytes(d.up)}
            </span>
          </div>
        ))}
      </div>
      <div class="col-list">
        <span class="muted small">Where this app goes</span>
        {places.length === 0 && <span class="muted small">no countries</span>}
        {places.map((p) => (
          <div key={p.cc + p.org} class="place-row small-13">
            <CC cc={p.cc} />
            <span class="ellipsis">{p.other ? "others" : p.org || countryName(p.cc)}</span>
            <span class="soft nowrap">
              ↓ {bytes(p.down)} · ↑ {bytes(p.up)}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function Where({ dest }: { dest: Destinations }) {
  return (
    <Card label="Destinations" title="Destinations">
      <div class="where">
        <div class="col-list">
          <span class="muted small">Countries</span>
          {dest.countries.length === 0 && <span class="muted small">none</span>}
          {dest.countries.map((c) => (
            <div key={c.cc} class="row-between small-13">
              <span>
                <b>{c.cc}</b> {c.other || c.cc === "--" ? "unknown or others" : countryName(c.cc)}
              </span>
              <span class="soft nowrap" title={`↑ ${bytes(c.up)}`}>↓ {bytes(c.down)}</span>
            </div>
          ))}
        </div>
        <div class="col-list">
          <span class="muted small">Organisations (ASN)</span>
          {dest.orgs.length === 0 && <span class="muted small">none</span>}
          {dest.orgs.map((o) => (
            <div key={o.asn} class="row-between small-13">
              <span class="ellipsis">
                {o.other || o.asn === "other" ? "other organisations" : o.org || "no name"}
                {!(o.other || o.asn === "other") && <span class="muted"> · AS{o.asn}</span>}
              </span>
              <span class="soft nowrap">↓ {bytes(o.down)}</span>
            </div>
          ))}
        </div>
        <div class="col-list">
          <span class="muted small">Domains</span>
          {dest.domains.length === 0 && <span class="muted small">none</span>}
          {dest.domains.map((dm) => (
            <div key={dm.name} class="row-between small-13">
              <span class="ellipsis">
                {dm.other ? "other domains" : dm.name || "no name"}
                {dm.app && <span class="muted"> · {appName(dm.app.name)}</span>}
              </span>
              <span class="soft nowrap">↓ {bytes(dm.down)}</span>
            </div>
          ))}
        </div>
      </div>
      <div class="foot soft small">
        {plural(dest.countries.filter(isRealCountry).length, "country", "countries")} in the period
      </div>
    </Card>
  );
}

function Flows({ mac, flows }: { mac: string; flows: FlowsPage }) {
  return (
    <Card label="Recent flows" title="Recent flows" aside={<a class="small" href={`#/flows?q=${encodeURIComponent("mac:" + mac)}`}>all of the client's flows</a>}>
      {flows.rows.length === 0 && <Empty>No flows yet.</Empty>}
      {flows.rows.map((f, i) => (
        <div key={i} class="flow-mini">
          <span class="muted">{time(f.end) === "-" ? time(f.start) : time(f.end)}</span>
          <span class="ellipsis">
            {f.name || f.remote}
            <span class="muted"> · {appName(f.app.name)}</span>
          </span>
          <span class="cc-plain">{f.cc || "--"}</span>
          <span class="soft nowrap">
            ↓ {bytes(f.down)} · ↑ {bytes(f.up)}
          </span>
        </div>
      ))}
    </Card>
  );
}

// Client rules: the rules that reach this device, from the same list as the
// Rules tab, its time limits, and its alerts.
function DeviceRules({ d }: { d: Device }) {
  const { rules } = useRules();
  const appNames = manifestName(useContext(IconContext));
  const mine = (rules ?? []).filter((r) => appliesTo(r, d.mac));
  const now = Date.now();
  // Time limits: today's minutes of each limited app, from the router's count.
  const usage = usePoll((s) => getJSON<Usage[]>(`/api/usage?mac=${enc(d.mac)}`, s), 60_000, [d.mac]);
  // The device's own alerts, newest first, the last three.
  const alerts = usePoll((s) => getJSON<AlertsState>("/api/alerts", s), 60_000, [d.mac]);
  const mineAlerts = (alerts.data?.alerts ?? []).filter((a) => a.mac === d.mac.toLowerCase()).slice(0, 3);
  return (
    <Card label="Client rules" title="Client rules" aside={<a class="small" href="#/rules">all rules</a>}>
      {!rules ? (
        <Loading />
      ) : mine.length === 0 ? (
        <Empty>No rules for this device.</Empty>
      ) : (
        mine.map((r) => (
          <div key={r.id} class="col-tight small-13">
            <span class="break">{describeRule(r, { device: () => deviceName(d), app: appNames })}</span>
            <span class={`state state-${stateOf(r, now).kind}`}><span class="dot" aria-hidden="true" />{stateOf(r, now).text}</span>
            {liveAllows(r, now, d.mac).map((a) => <span key={a.id} class="soft">{allowText(a)}</span>)}
          </div>
        ))
      )}
      {(usage.data ?? []).map((u) => (
        <div key={u.rule} class="row-wrap small-13">
          <span class="break">{usageLabel(u, appNames)}:</span>
          <span class={u.spentUntil ? "state state-active" : "soft"}>{usageText(u)}</span>
        </div>
      ))}
      {mineAlerts.length > 0 && <span class="small strong">Alerts</span>}
      {mineAlerts.map((a) => <span key={a.id} class="small-13 break">{alertText(a, () => deviceName(d))}</span>)}
    </Card>
  );
}
