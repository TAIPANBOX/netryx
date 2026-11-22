// A client's name anywhere on the page opens its card beside it: the card
// floats (several can be open to compare), and "Open full" leads to the
// client's full view. A click with a modifier, or a middle click, follows the
// link as a link does.

import { h } from "preact";
import type { AppUsage, Device, NetworkView } from "../types";
import { getJSON, mac as enc, usePoll } from "../api";
import { AppIcon, Empty, Loading } from "../ui";
import { appName, bytes, date, rate, time, groupName } from "../format";
import { whereConnected } from "../client360";
import { useCards } from "./windows";
import type { Group } from "../types";

/** The link that opens a client's card. */
export function ClientLink({ mac, name, title, class: cls }: { mac: string; name: string; title?: string; class?: string }) {
  const { open } = useCards();
  const href = `#/client/${enc(mac)}`;
  return (
    <a
      href={href}
      class={cls ? `entity-link ${cls}` : "entity-link"}
      onClick={(e) => {
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        e.stopPropagation();
        open(`client:${mac}`, { title: title ?? name, body: () => <ClientCard mac={mac} /> }, e.currentTarget as Element);
      }}
    >
      {name}
    </a>
  );
}

/** The floating card: who the device is, where it is connected, what it does now. */
export function ClientCard({ mac }: { mac: string }) {
  const { close } = useCards();
  const base = `/api/devices/${enc(mac)}`;
  const dev = usePoll((s) => getJSON<Device>(`${base}?window=3m`, s), 10_000, [mac]);
  const apps = usePoll((s) => getJSON<AppUsage[]>(`${base}/apps?range=1h&window=3m`, s), 15_000, [mac]);
  const net = usePoll((s) => getJSON<NetworkView>("/api/network", s), 30_000, []);
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  const d = dev.data;
  if (dev.status === 404) return <Empty>The router knows no device with MAC {mac.toUpperCase()}.</Empty>;
  if (dev.error && !d) return <p class="bad-text small">The router is not answering: {dev.error}.</p>;
  if (!d) return <Loading />;
  const busy = (apps.data ?? [])
    .filter((a) => !a.other && a.recent && a.recent.down + a.recent.up > 0)
    .sort((a, b) => (b.recent!.down + b.recent!.up) - (a.recent!.down + a.recent!.up))
    .slice(0, 3);
  const rows: [string, string][] = [
    ["State", d.online ? "online now" : `last seen ${date(d.last)} ${time(d.last)}`],
    ["Connected", whereConnected(d, net.data)],
    ["Address", [...(d.ipv4 ?? []), ...(d.ipv6 ?? [])].join(", ") || "-"],
    ["MAC", d.mac.toUpperCase() + (d.random ? " (random)" : "")],
    ["Vendor", d.vendor || "-"],
    ["Group", d.group ? groupName(d.group, groups) : "no group"],
    ["Now, 3 min avg", `↓ ${rate(d.recent?.down ?? 0)} · ↑ ${rate(d.recent?.up ?? 0)}`],
    ["Last 24 h", `↓ ${bytes(d.last24h.down)} · ↑ ${bytes(d.last24h.up)}`],
  ];
  return (
    <div class="col-tight client-card">
      <div class="kv-mini">
        {rows.map(([k, v]) => [<span key={k + "k"}>{k}</span>, <span key={k + "v"} class="break">{v}</span>])}
      </div>
      {(d.quarantined || d.isolate) && (
        <span class="tag">{d.quarantined ? "in quarantine" : "isolated"}</span>
      )}
      <span class="muted small">Busiest over 3 min</span>
      {busy.length === 0 ? (
        <span class="muted small">nothing moving now</span>
      ) : (
        busy.map((a) => (
          <a key={a.app.id} href={`#/app/${encodeURIComponent(a.app.id)}`} class="app-row row-link">
            <AppIcon id={a.app.id} name={appName(a.app.name)} size={22} />
            <span class="ellipsis">{appName(a.app.name)}</span>
            <span class="soft small right nowrap">↓ {rate(a.recent!.down)}</span>
          </a>
        ))
      )}
      <div class="row-wrap">
        <a class="btn" href={`#/client/${enc(mac)}`} onClick={(e) => { if (!e.metaKey && !e.ctrlKey && !e.shiftKey && e.button === 0) close(`client:${mac}`); }}>Open full</a>
        <a class="btn" href={`#/flows?q=${encodeURIComponent("mac:" + mac)}`}>Show flows</a>
      </div>
    </div>
  );
}
