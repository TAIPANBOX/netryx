// A client's name anywhere on the page opens its card beside it: the card
// floats (several can be open to compare), and "Open full" leads to the
// client's full view. A click with a modifier, or a middle click, follows the
// link as a link does.

import { h, Fragment, type ComponentChildren } from "preact";
import { crumbsFrom, entityHref, withFrom, type Crumb, type Entity } from "../entity";
import { manifestName } from "../rules";
import type { AppUsage, ClientRef, Device, NetworkView } from "../types";
import { clientsLine } from "../house";
import { getJSON, mac as enc, usePoll } from "../api";
import { AppIcon, Empty, IconContext, Loading } from "../ui";
import { useContext } from "preact/hooks";
import { appName, bytes, clientName, date, rate, time, groupName } from "../format";
import { whereConnected } from "../client360";
import { useCards } from "./windows";
import type { Group } from "../types";

/** The link that opens a client's card. */
export function ClientLink({ mac, name, title, from = "", class: cls }: { mac: string; name: string; title?: string; from?: string; class?: string }) {
  const { open } = useCards();
  const href = withFrom(`#/client/${enc(mac)}`, from);
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

/**
 * Any name as a link to its screen (a client's opens its card): no underline
 * until hovered or focused. A name that leads nowhere stays text.
 */
export function EntityLink({ e, children, from = "", class: cls, title }: { e: Entity; children: ComponentChildren; from?: string; class?: string; title?: string }) {
  if (e.kind === "client") return <ClientLink mac={e.mac} name={String(children)} from={from} class={cls} title={title} />;
  const href = entityHref(e);
  if (!href) return <span class={cls} title={title}>{children}</span>;
  return <a class={cls ? `entity-link ${cls}` : "entity-link"} href={withFrom(href, from)} title={title}>{children}</a>;
}

/** The first clients of a row by name, each opening its card, and how many more. */
export function ClientNames({ n, top, from = "" }: { n: number; top: ClientRef[]; from?: string }) {
  const named = top.filter((c) => !c.other);
  if (n === 0 || named.length === 0) return <span class="soft">{clientsLine(n, top)}</span>;
  const rest = n - named.length;
  return (
    <span class="soft">
      {named.map((c, i) => (
        <Fragment key={c.mac}>
          {i > 0 && ", "}
          <ClientLink mac={c.mac} name={clientName(c)} from={from} />
        </Fragment>
      ))}
      {rest > 0 && ` and ${rest} more`}
    </span>
  );
}

/**
 * Where a view sits, as a chain of chips: the path the owner took (or the
 * screen it belongs under), the thing itself, and the things it belongs to.
 */
export function ChipChain({ from, base, last, parents = [] }: { from?: string; base: Crumb[]; last: string; parents?: Crumb[] }) {
  const appNames = manifestName(useContext(IconContext));
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  const taken = crumbsFrom(from, { app: appNames, group: (id) => groupName(id, groups) });
  const path = taken.length ? taken : base;
  return (
    <nav class="chips" aria-label="Where this is">
      {path.map((c) => (
        <Fragment key={c.href}>
          <a class="chip" href={c.href}>{c.label}</a>
          <span class="chip-sep" aria-hidden="true">›</span>
        </Fragment>
      ))}
      <span class="chip chip-here" aria-current="page">{last}</span>
      {parents.length > 0 && <span class="muted small chip-note">belongs to</span>}
      {parents.map((c) => (
        <a key={c.href} class="chip chip-parent" href={c.href}>{c.label}</a>
      ))}
    </nav>
  );
}
