// Pieces of the client's full view (Client360.tsx) for now: four tiles, the
// last five minutes by app, the active apps, the connections and where they
// go, from the live view polled every 2 s.

import { entityHref } from "../entity";
import { EntityLink } from "./entity";
import { h, type ComponentChildren } from "preact";
import { useContext, useRef } from "preact/hooks";
import type { App, AppUsage, Live, LiveApp, LiveConn, LivePlace } from "../types";
import { windowLabel, type Win } from "../query";
import { useFlip, useSteadyRows } from "./table";

import { TimeChart, type Layer } from "../charts";
import { Pills, Card, CC, Empty, AppIcon, IconContext } from "../ui";
import { appName, category, countryName, duration, nameSource, num, plural, rate, ratePair } from "../format";
import { LAYER_COLORS, OTHER_COLOR, UP_COLOR } from "../view";
import { layerColor } from "../icons";
import { appsTileSub, connKeys, layerLabel, liveChart, liveSummary, placeName, sparkPath, speedTile, whereText, LIVE_TICKS } from "../live";

import { BlockApp, CutButton, CUT_NOTE } from "./controls";
import { useSession } from "../ui";

/** Connections listed; the API sends the busiest 50. */
const CONNS_SHOWN = 12;

export function LiveTiles({ l, recent, win }: { l: Live; recent?: { down: number; up: number }; win: Win }) {
  const s = liveSummary(l);
  const conns = l.conns ?? [];
  const hasLan = (l.places ?? []).some((p) => p.cc === "LAN");
  const speed = speedTile(recent, l.rate, windowLabel(win));
  const tiles = [
    { label: "Speed", value: speed.value, sub: speed.sub },
    {
      label: "Active connections",
      value: num(l.flows),
      sub: conns.length === 0 ? "none" : `${s.tcp} TCP · ${s.udp} UDP` + (l.flows > conns.length ? ` among the ${conns.length} fastest` : ""),
    },
    { label: "Active apps", value: num(s.apps), sub: appsTileSub(s) },
    {
      label: "Destinations now",
      value: plural(s.countries, "country", "countries"),
      sub: s.mainCC ? `main: ${countryName(s.mainCC)}` : hasLan ? "local network only" : "nowhere",
    },
  ];
  return (
    <section aria-label="Now" class="tiles live-tiles">
      {tiles.map((t) => (
        <div key={t.label} class="tile">
          <span class="tile-label">{t.label}</span>
          <span class="tile-value">{t.value}</span>
          <span class="tile-sub">{t.sub}</span>
        </div>
      ))}
    </section>
  );
}

export function LiveTraffic({ l, seen }: { l: Live; seen: Map<string, App> }) {
  const manifest = useContext(IconContext);
  const c = liveChart(l.series, 3);
  const used = new Set<string>();
  const colors = c.keys.map((k, i) => layerColor(k, manifest, used, LAYER_COLORS[i]));
  // Bottom of the stack first: the rest, then the smaller apps, the largest on top.
  const layers: Layer[] = [];
  const layerKeys: (string | undefined)[] = [];
  if (c.other) {
    layers.push({ label: "others", color: OTHER_COLOR, values: c.otherDown, id: "other" });
    layerKeys.push(undefined);
  }
  for (let i = c.keys.length - 1; i >= 0; i--) {
    const k = c.keys[i];
    layers.push({ label: layerLabel(k, seen, manifest), color: colors[i], values: c.down[k], id: k });
    layerKeys.push(k);
  }
  // A legend entry names its app, and leads to it.
  const legend: { label: string; color: string; node?: ComponentChildren; id?: string }[] = layers
    .map((x, i) => ({ label: x.label, color: x.color, id: x.id, node: layerKeys[i] ? <EntityLink e={{ kind: "app", id: layerKeys[i]! }}>{x.label}</EntityLink> : undefined }))
    .reverse();
  legend.push({ label: "up", color: UP_COLOR });
  const step = Math.round(l.stepSec);
  // Each column's start, for the crosshair: the chart ends at the newest point.
  const t = c.up.map((_, i) => new Date(c.end - (c.up.length - i) * c.colSec * 1000).toISOString());
  return (
    <Card label="Live traffic">
      <div class="card-head">
        <h2>Last 5 minutes, by app</h2>
        <span class="muted small">above the axis down, below it up{step > 0 ? ` · step ${step} s` : ""}</span>
      </div>
      {c.up.length === 0 ? (
        <Empty>The chart appears in a few seconds.</Empty>
      ) : (
        <TimeChart layers={layers} up={c.up} t={t} stepSec={c.colSec} viewH={220} axis={165} downH={155} upH={50} label="Live traffic chart of the device over 5 minutes" legend={legend} ticks={LIVE_TICKS} />
      )}
    </Card>
  );
}

export function LiveApps({ apps, mac, recent, win }: { apps: LiveApp[]; mac: string; recent: AppUsage[]; win: Win }) {
  const manifest = useContext(IconContext);
  const avg = new Map(recent.map((r) => [r.app.id, r.recent ?? { up: 0, down: 0 }]));
  const avgOf = (a: LiveApp) => avg.get(a.app.id) ?? { up: a.up, down: a.down };
  const { rows, sort, pick, holdProps } = useSteadyRows(
    "client-live-apps",
    apps,
    (a) => a.app.id,
    { avg: (a) => avgOf(a).down + avgOf(a).up, now: (a) => a.down + a.up, name: (a) => appName(a.app.name) },
    { col: "avg", dir: "desc" },
  );
  const list = useRef<HTMLDivElement>(null);
  useFlip(list);
  return (
    <Card
      label="Active apps"
      title="Active apps now"
      aside={
        <Pills
          label="Order the apps by"
          inset
          value={sort.col}
          onPick={(c) => pick(c, c === "name" ? "asc" : "desc")}
          items={[{ id: "avg", label: `${windowLabel(win)} average` }, { id: "now", label: "speed now" }, { id: "name", label: "name" }]}
        />
      }
    >
      {apps.length === 0 && <Empty>The device is not sending anything now.</Empty>}
      <div class="col-tight" ref={list} {...holdProps}>
      {rows.map((a) => {
        const name = appName(a.app.name);
        const d = sparkPath(a.spark);
        const m = avgOf(a);
        return (
          <div key={a.app.id} data-key={a.app.id} class="live-app">
            <AppIcon id={a.app.id} name={name} size={40} />
            <div class="col-tight">
              <EntityLink e={{ kind: "app", id: a.app.id }} class="strong ellipsis">{name}</EntityLink>
              <span class="muted small">{name.includes(" · ") ? "by organisation" : <EntityLink e={{ kind: "category", id: a.app.category }}>{category(a.app.category) || "-"}</EntityLink>}</span>
              <BlockApp appId={a.app.id} name={name} mac={mac} />
            </div>
            <div class="col-tight live-rate" title={`average over the last ${windowLabel(win)}; right now ↓ ${rate(a.down)} · ↑ ${rate(a.up)}`}>
              <span class="strong nowrap">↓ {rate(m.down)}</span>
              <span class="soft small nowrap">↑ {rate(m.up)}</span>
              <span class="muted small nowrap">now ↓ {rate(a.down)}</span>
            </div>
            <svg viewBox="0 0 120 28" preserveAspectRatio="none" role="img" aria-label={`trend of ${name}`} class="spark">
              {d && <path d={d} fill="none" stroke={layerColor(a.app.id, manifest, new Set(), LAYER_COLORS[0])} stroke-width="2" />}
            </svg>
            <span class="soft live-conns nowrap">{num(a.flows)} conn.</span>
            <span class="small-13 ellipsis live-domain">
              <EntityLink e={{ kind: "domain", name: a.domain }}>{a.domain || "no name"}</EntityLink>
              {a.src && a.domain && <span class="muted"> · {nameSource(a.src)}</span>}
            </span>
            <span class="small-13 soft ellipsis live-where"><EntityLink e={{ kind: "country", cc: a.cc }}>{whereText(a.cc, a.org)}</EntityLink></span>
          </div>
        );
      })}
      </div>
    </Card>
  );
}

export function LiveConns({ mac, conns, flows }: { mac: string; conns: LiveConn[]; flows: number }) {
  const shown = conns.slice(0, CONNS_SHOWN);
  const keys = connKeys(shown);
  const rest = Math.max(0, flows - shown.length);
  const all = `#/flows?q=${encodeURIComponent("mac:" + mac)}`;
  const { canWrite } = useSession();
  return (
    <Card label="Connections now" title="Connections now" aside={<a class="small" href={all}>all of the device's flows</a>}>
      {shown.length === 0 && <Empty>No open connections.</Empty>}
      {shown.map((c, i) => {
        const p = ratePair(c.down, c.up);
        const label = c.name || c.org || "no name";
        return (
          <div key={keys[i]} class={canWrite && c.local && c.remote ? "live-conn live-conn-w" : "live-conn"}>
            <AppIcon id={c.app.id} name={appName(c.app.name)} size={28} />
            <span class="ellipsis">
              {c.name ? <EntityLink e={{ kind: "domain", name: c.name }}>{label}</EntityLink> : <EntityLink e={{ kind: "network", asn: c.asn }}>{label}</EntityLink>}
              {c.org && c.name && <span class="muted"> · <EntityLink e={{ kind: "network", asn: c.asn }}>{c.org}</EntityLink></span>}
            </span>
            {entityHref({ kind: "country", cc: c.cc }) ? (
              <a title={placeName(c)} href={entityHref({ kind: "country", cc: c.cc })} class="entity-link">
                <CC cc={c.cc} />
              </a>
            ) : (
              <span title={placeName(c)}>
                <CC cc={c.cc} />
              </span>
            )}
            <span class="nowrap live-conn-rate">
              ↓ {p.down} <span class="muted">↑ {p.up}</span> <span class="muted small">{p.unit}</span>
            </span>
            <span class="muted right nowrap live-conn-dur">{duration(c.durSec)}</span>
            <CutButton mac={mac} c={c} />
          </div>
        );
      })}
      {canWrite && shown.length > 0 && <p class="small muted rule-note">Cut: {CUT_NOTE}.</p>}
      {rest > 0 && (
        <a class="small foot" href={all}>
          and {plural(rest, "more connection", "more connections")} in Flows
        </a>
      )}
    </Card>
  );
}

export function LivePlaces({ places }: { places: LivePlace[] }) {
  const total = places.reduce((a, p) => a + p.down, 0);
  return (
    <Card label="Destinations now" title="Destinations now">
      {places.length === 0 && <Empty>The device is not sending anywhere now.</Empty>}
      {places.map((p) => (
        <div key={p.cc + "|" + p.org} class="live-place">
          <CC cc={p.cc} />
          <div class="col-tight">
            <span class="small-13 ellipsis">
              <EntityLink e={{ kind: "country", cc: p.cc }}>{placeName(p)}</EntityLink>
              {p.org && <span class="muted small"> · <EntityLink e={{ kind: "network", asn: p.asn ?? 0 }}>{p.org}</EntityLink></span>}
            </span>
            <div class="bars bars-thin">
              <span class="bar bar-down" style={{ width: `${total > 0 ? ((100 * p.down) / total).toFixed(2) : "0"}%` }} />
            </div>
          </div>
          <span class="soft small right nowrap">↓ {rate(p.down)}</span>
        </div>
      ))}
      <div class="foot muted small">
        Icons: the built-in Simple Icons set (CC0) and favicons the router fetches itself from the brands' sites. The browser takes nothing from the internet.
      </div>
    </Card>
  );
}
