// Clients: every device the router knows, as a table with the columns of the
// spec's section 2.2. Not in the mockup; styled like its tables.

import { filterHash } from "../entity";
import { EntityLink } from "./entity";
import { ClientLink } from "./entity";
import { HourSparkline } from "../charts";
import { COMPARE_MAX, compareHash, pickCompare } from "../compare";
import { h, type ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { windowLabel, type Win } from "../query";
import { SortTh, useFlip, useSteadyRows } from "./table";
import type { Device, Group } from "../types";
import { getJSON, usePoll } from "../api";
import { Header, Status, Loading, Empty, AppIcon } from "../ui";
import { bytes, rate, time, date, band, appName, num, groupName } from "../format";

export function deviceName(d: Pick<Device, "name" | "label" | "hostname" | "mac">): string {
  return d.label || d.name || d.hostname || d.mac;
}

/** How a device is attached: "Wi-Fi 5 GHz · "Home" · -58 dBm", or the port. */
export function linkText(d: Device): string {
  const l = d.link;
  if (l.kind === "wifi") {
    const parts = ["Wi-Fi" + (l.band ? " " + band(l.band) : "")];
    if (l.ssid) parts.push(`"${l.ssid}"`);
    if (l.signal) parts.push(`${l.signal} dBm`);
    return parts.join(" · ");
  }
  if (l.kind) return l.iface ? `${l.kind} · ${l.iface}` : l.kind;
  return l.iface ? `cable or unknown · ${l.iface}` : "unknown";
}

function matches(d: Device, q: string, groups: Group[]): boolean {
  if (!q) return true;
  const hay = [deviceName(d), d.hostname, d.vendor, d.mac, d.group, groupName(d.group, groups), ...(d.ipv4 ?? []), ...(d.ipv6 ?? [])].join(" ").toLowerCase();
  return q.toLowerCase().split(/\s+/).every((w) => hay.includes(w));
}

const recentTotal = (d: Device) => (d.recent ? d.recent.down + d.recent.up : 0);

export function Clients({ q0, win, onWin }: { q0: string; win: Win; onWin: (w: Win) => void }) {
  const polled = usePoll((s) => getJSON<Device[]>(`/api/devices?window=${win}`, s), 10_000, [win]);
  const groupsPolled = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []);
  const groups = groupsPolled.data ?? [];
  const [q, setQ] = useState(q0);
  const [sel, setSel] = useState<string[]>([]);
  // The search is kept in the address (without a new history entry per key),
  // so Back and a shared link keep it.
  useEffect(() => {
    const h = filterHash("clients", { q });
    if (location.hash !== h) history.replaceState(null, "", h);
  }, [q]);
  const all = polled.data ?? [];
  const found = all.filter((d) => matches(d, q, groups));
  const online = all.filter((d) => d.online).length;
  // The busiest over the window first by default; any column sorts, and
  // rows move at most every 30 s.
  const { rows, sort, pick, holdProps } = useSteadyRows(
    "clients",
    found,
    (d) => d.mac,
    {
      state: (d) => Number(d.online),
      name: (d) => deviceName(d),
      vendor: (d) => d.vendor,
      now: recentTotal,
      day: (d) => d.last24h.down + d.last24h.up,
      countries: (d) => d.countryCount,
      last: (d) => Date.parse(d.last) || 0,
    },
    { col: "now", dir: "desc" },
  );
  const body = useRef<HTMLTableSectionElement>(null);
  useFlip(body);
  const th = (label: ComponentChildren, col: string, first: "asc" | "desc" = "desc", num = false) => <SortTh label={label} col={col} sort={sort} pick={pick} first={first} num={num} />;

  return (
    <div class="page">
      <Header section="clients" note="Traffic over 24 h" win={win} onWin={onWin} />
      <main class="main">
        <Status polled={polled} />
        <div class="toolbar">
          <h1>Clients</h1>
          {polled.data && (
            <span class="muted small">
              {num(online)} online of {num(all.length)}
            </span>
          )}
          <div class="field grow">
            <label for="clients-q" class="muted small">Search</label>
            <input id="clients-q" type="search" class="input" value={q} placeholder="name, IP, MAC, vendor, group" onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
          </div>
        </div>
        {sel.length > 0 && (
          <div class="toolbar compare-bar" role="status">
            <span class="small">{sel.length} of {COMPARE_MAX} picked to compare</span>
            {sel.length >= 2 ? <a class="btn" href={compareHash(sel)}>Compare</a> : <span class="muted small">pick one more</span>}
            <button type="button" class="btn-mini" onClick={() => setSel([])}>Clear</button>
          </div>
        )}
        {!polled.data ? (
          <Loading />
        ) : (
          <section aria-label="Clients table" class="card card-flush">
            <div class="scroll-x">
              <table class="table clients-table">
                <thead>
                  <tr>
                    <th><span class="sr">Compare</span></th>
                    {th("State", "state")}
                    {th("Name", "name", "asc")}
                    {th(`Now, ${windowLabel(win)} avg ↓ · ↑`, "now", "desc", true)}
                    {th("24 h ↓ · ↑", "day", "desc", true)}
                    <th>Last hour</th>
                    <th>App</th>
                    {th("Vendor", "vendor", "asc")}
                    <th>IPv4 · IPv6</th>
                    <th>MAC</th>
                    <th>Connection</th>
                    <th>Group</th>
                    {th("Countries", "countries", "desc", true)}
                    {th("Last seen", "last")}
                  </tr>
                </thead>
                <tbody ref={body} {...holdProps}>
                  {rows.map((d) => (
                    <tr key={d.mac} data-key={d.mac}>
                      <td>
                        <input
                          type="checkbox"
                          aria-label={`Compare ${deviceName(d)}`}
                          checked={sel.includes(d.mac)}
                          disabled={!sel.includes(d.mac) && sel.length >= COMPARE_MAX}
                          onChange={() => setSel((s) => pickCompare(s, d.mac))}
                        />
                      </td>
                      <td>
                        <span class={d.online ? "dot dot-on" : "dot dot-off"} aria-hidden="true" />
                        <span class="sr">{d.online ? "online" : "offline"}</span>
                      </td>
                      <td>
                        <ClientLink mac={d.mac} name={deviceName(d)} class="strong" />
                        {d.hostname && d.hostname !== deviceName(d) && <div class="muted small">{d.hostname}</div>}
                      </td>
                      <td class="num small" title={`average over the last ${windowLabel(win)}; right now ↓ ${rate(d.rate.down)} · ↑ ${rate(d.rate.up)}`}>
                        ↓ {rate(d.recent?.down ?? 0)}
                        <br />{" "}↑ {rate(d.recent?.up ?? 0)}
                      </td>
                      <td class="num small">
                        ↓ {bytes(d.last24h.down)}
                        <br />{" "}↑ {bytes(d.last24h.up)}
                      </td>
                      <td><HourSparkline s={d.spark} /></td>
                      <td>
                        {d.topApp ? (
                          <span class="app-cell">
                            <AppIcon id={d.topApp.id} name={appName(d.topApp.name)} size={20} />
                            <EntityLink e={{ kind: "app", id: d.topApp.id }}>{appName(d.topApp.name)}</EntityLink>
                          </span>
                        ) : (
                          "-"
                        )}
                      </td>
                      <td class="soft">{d.vendor || "-"}</td>
                      <td class="mono small">
                        {[...(d.ipv4 ?? []), ...(d.ipv6 ?? [])].map((ip) => (
                          <div key={ip}>{ip}</div>
                        ))}
                        {!(d.ipv4?.length || d.ipv6?.length) && "-"}
                      </td>
                      <td class="mono small">
                        {d.mac.toUpperCase()}
                        {d.random && <span class="tag" title="The device made this address up itself; its vendor cannot be told from it">random MAC</span>}
                      </td>
                      <td class="soft small">{linkText(d)}</td>
                      <td class="soft">{d.group ? <EntityLink e={{ kind: "group", id: d.group }}>{groupName(d.group, groups)}</EntityLink> : "-"}</td>
                      <td class="num">{d.countryCount}</td>
                      <td class="soft small">{d.online ? "online now" : `${date(d.last)} ${time(d.last)}`}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {rows.length === 0 && <Empty>{q ? "Nothing found." : "The router has not seen any device yet."}</Empty>}
          </section>
        )}
      </main>
    </div>
  );
}
