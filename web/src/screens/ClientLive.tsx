// Client card, mode "Now": what the device is doing right now, polled
// every 2 s while the page is visible. Layout: the device and the live line, four tiles,
// the last five minutes by app, the active apps, the connections and where
// they go.

import { h, type ComponentChildren } from "preact";
import { useContext, useRef } from "preact/hooks";
import type { App, Device, Live, LiveApp, LiveConn, LivePlace } from "../types";
import { getJSON, mac as enc, usePoll } from "../api";
import { StackedArea, Legend, type Layer } from "../charts";
import { Header, Card, CC, Status, Loading, Empty, AppIcon, IconContext } from "../ui";
import { appName, category, clock, countryName, duration, nameSource, num, plural, rate, ratePair } from "../format";
import { LAYER_COLORS, OTHER_COLOR, UP_COLOR } from "../view";
import { layerColor } from "../icons";
import { appsTileSub, connKeys, layerLabel, liveChart, liveSummary, placeName, sparkPath, whereText, LIVE_TICKS } from "../live";
import { deviceName, linkText } from "./Clients";
import { BlockApp, CutButton, DeviceActions, DeviceSecurity, CUT_NOTE } from "./controls";
import { useSession } from "../ui";

/** How often the card asks the router; the router syncs at 2 s while it is asked. */
const EVERY_MS = 2_000;
/** Connections listed; the API sends the busiest 50. */
const CONNS_SHOWN = 12;

export function ClientLive({ mac, modes }: { mac: string; modes: ComponentChildren }) {
  const base = `/api/devices/${enc(mac)}`;
  const dev = usePoll((s) => getJSON<Device>(base, s), 10_000, [mac]);
  const polled = usePoll((s) => getJSON<Live>(`${base}/live`, s), EVERY_MS, [mac]);
  // Apps seen in this view, so a chart layer keeps its name after the app
  // stops and leaves the apps list.
  const seen = useRef(new Map<string, App>());
  const l = polled.data;
  for (const a of l?.apps ?? []) seen.current.set(a.app.id, a.app);
  for (const c of l?.conns ?? []) seen.current.set(c.app.id, c.app);
  const name = dev.data ? deviceName(dev.data) : mac;
  const notFound = polled.status === 404 && !l;

  return (
    <div class="page">
      <Header section="clients" pills={modes} />
      <main class="main">
        <div class="crumbs">
          <a href="#/clients">Clients</a> / {name}
        </div>
        {notFound ? <Empty>The router knows no device with MAC {mac.toUpperCase()}.</Empty> : <Status polled={polled} />}
        {!l ? (
          notFound ? null : <Loading />
        ) : (
          <div class="stack">
            <LiveDevice d={dev.data} name={name} at={polled.at} stale={!!polled.error} />
            <LiveTiles l={l} />
            <LiveTraffic l={l} seen={seen.current} />
            <LiveApps apps={l.apps ?? []} mac={mac} />
            <div class="grid-2">
              <LiveConns mac={mac} conns={l.conns ?? []} flows={l.flows} />
              <LivePlaces places={l.places ?? []} />
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

function LiveDevice({ d, name, at, stale }: { d?: Device; name: string; at?: number; stale: boolean }) {
  const meta = d ? [...(d.ipv4 ?? []).slice(0, 1), linkText(d), d.online ? "online" : "offline"].filter(Boolean).join(" · ") : "";
  return (
    <section aria-label="Device" class="card live-device">
      <div class="device-id">
        <div class="device-icon device-icon-sm">
          <svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#9BD0FF" stroke-width="1.8" aria-hidden="true">
            <rect x="2" y="4" width="20" height="13" rx="2" />
            <path d="M8 21h8M12 17v4" />
          </svg>
        </div>
        <div class="col-tight">
          <h1>{name}</h1>
          {meta && <span class="soft small">{meta}</span>}
        </div>
      </div>
      <div class="live-line" role="status">
        <span class={stale ? "dot dot-off" : "dot dot-live"} aria-hidden="true" />
        <span>
          {stale ? "Live: the router is not answering" : "Live"} · updated every {EVERY_MS / 1000} s
          {at ? ` · last at ${clock(new Date(at).toISOString())}` : ""}
        </span>
      </div>
      {d && <DeviceActions d={d} />}
      {d && <DeviceSecurity d={d} />}
    </section>
  );
}

function LiveTiles({ l }: { l: Live }) {
  const s = liveSummary(l);
  const r = ratePair(l.rate.down, l.rate.up);
  const conns = l.conns ?? [];
  const step = Math.round(l.stepSec);
  const hasLan = (l.places ?? []).some((p) => p.cc === "LAN");
  const tiles = [
    { label: "Speed now", value: `↓ ${r.down} · ↑ ${r.up}`, sub: `${r.unit}, ${step > 0 ? `over the last ${step} s` : "over the last interval"}` },
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

function LiveTraffic({ l, seen }: { l: Live; seen: Map<string, App> }) {
  const manifest = useContext(IconContext);
  const c = liveChart(l.series, 3);
  const used = new Set<string>();
  const colors = c.keys.map((k, i) => layerColor(k, manifest, used, LAYER_COLORS[i]));
  // Bottom of the stack first: the rest, then the smaller apps, the largest on top.
  const layers: Layer[] = [];
  if (c.other) layers.push({ label: "others", color: OTHER_COLOR, values: c.otherDown });
  for (let i = c.keys.length - 1; i >= 0; i--) {
    const k = c.keys[i];
    layers.push({ label: layerLabel(k, seen, manifest), color: colors[i], values: c.down[k] });
  }
  const legend = [...layers].reverse().map((x) => ({ label: x.label, color: x.color }));
  legend.push({ label: "up", color: UP_COLOR });
  const step = Math.round(l.stepSec);
  return (
    <Card label="Live traffic">
      <div class="card-head">
        <h2>Last 5 minutes, by app</h2>
        <span class="muted small">above the axis down, below it up{step > 0 ? ` · step ${step} s` : ""}</span>
      </div>
      {c.up.length === 0 ? (
        <Empty>The chart appears in a few seconds.</Empty>
      ) : (
        <StackedArea layers={layers} up={c.up} viewH={220} axis={165} downH={155} upH={50} label="Live traffic chart of the device over 5 minutes" />
      )}
      <Legend items={legend} ticks={LIVE_TICKS} />
    </Card>
  );
}

function LiveApps({ apps, mac }: { apps: LiveApp[]; mac: string }) {
  const manifest = useContext(IconContext);
  return (
    <Card label="Active apps" title="Active apps now" aside={<span class="muted small">speed now · connections · main domain · where</span>}>
      {apps.length === 0 && <Empty>The device is not sending anything now.</Empty>}
      {apps.map((a) => {
        const name = appName(a.app.name);
        const d = sparkPath(a.spark);
        return (
          <div key={a.app.id} class="live-app">
            <AppIcon id={a.app.id} name={name} size={40} />
            <div class="col-tight">
              <span class="strong ellipsis">{name}</span>
              <span class="muted small">{name.includes(" · ") ? "by organisation" : category(a.app.category) || "-"}</span>
              <BlockApp appId={a.app.id} name={name} mac={mac} />
            </div>
            <div class="col-tight live-rate">
              <span class="strong nowrap">↓ {rate(a.down)}</span>
              <span class="soft small nowrap">↑ {rate(a.up)}</span>
            </div>
            <svg viewBox="0 0 120 28" preserveAspectRatio="none" role="img" aria-label={`trend of ${name}`} class="spark">
              {d && <path d={d} fill="none" stroke={layerColor(a.app.id, manifest, new Set(), LAYER_COLORS[0])} stroke-width="2" />}
            </svg>
            <span class="soft live-conns nowrap">{num(a.flows)} conn.</span>
            <span class="small-13 ellipsis live-domain">
              {a.domain || "no name"}
              {a.src && a.domain && <span class="muted"> · {nameSource(a.src)}</span>}
            </span>
            <span class="small-13 soft ellipsis live-where">{whereText(a.cc, a.org)}</span>
          </div>
        );
      })}
    </Card>
  );
}

function LiveConns({ mac, conns, flows }: { mac: string; conns: LiveConn[]; flows: number }) {
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
              {label}
              {c.org && c.name && <span class="muted"> · {c.org}</span>}
            </span>
            <span title={placeName(c)}>
              <CC cc={c.cc} />
            </span>
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

function LivePlaces({ places }: { places: LivePlace[] }) {
  const total = places.reduce((a, p) => a + p.down, 0);
  return (
    <Card label="Destinations now" title="Destinations now">
      {places.length === 0 && <Empty>The device is not sending anywhere now.</Empty>}
      {places.map((p) => (
        <div key={p.cc + "|" + p.org} class="live-place">
          <CC cc={p.cc} />
          <div class="col-tight">
            <span class="small-13 ellipsis">
              {placeName(p)}
              {p.org && <span class="muted small"> · {p.org}</span>}
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
