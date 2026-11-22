// Clients: every device the router knows, as a table with the columns of the
// spec's section 2.2. Not in the mockup; styled like its tables.

import { h } from "preact";
import { useState } from "preact/hooks";
import type { Device, Group } from "../types";
import { getJSON, mac, usePoll } from "../api";
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

export function Clients() {
  const polled = usePoll((s) => getJSON<Device[]>("/api/devices", s), 10_000, []);
  const groupsPolled = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []);
  const groups = groupsPolled.data ?? [];
  const [q, setQ] = useState("");
  const all = polled.data ?? [];
  // Online first, then by traffic: the devices that matter now are on top.
  const rows = all
    .filter((d) => matches(d, q, groups))
    .sort((a, b) => Number(b.online) - Number(a.online) || b.last24h.down + b.last24h.up - (a.last24h.down + a.last24h.up));
  const online = all.filter((d) => d.online).length;

  return (
    <div class="page">
      <Header section="clients" note="Traffic over 24 h" />
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
        {!polled.data ? (
          <Loading />
        ) : (
          <section aria-label="Clients table" class="card card-flush">
            <div class="scroll-x">
              <table class="table clients-table">
                <thead>
                  <tr>
                    <th>State</th>
                    <th>Name</th>
                    <th>Vendor</th>
                    <th>IPv4 · IPv6</th>
                    <th>MAC</th>
                    <th>Connection</th>
                    <th>Group</th>
                    <th class="num">Now ↓ · ↑</th>
                    <th class="num">24 h ↓ · ↑</th>
                    <th>App</th>
                    <th class="num">Countries</th>
                    <th>Last seen</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((d) => (
                    <tr key={d.mac}>
                      <td>
                        <span class={d.online ? "dot dot-on" : "dot dot-off"} aria-hidden="true" />
                        <span class="sr">{d.online ? "online" : "offline"}</span>
                      </td>
                      <td>
                        <a href={`#/client/${mac(d.mac)}`} class="strong">{deviceName(d)}</a>
                        {d.hostname && d.hostname !== deviceName(d) && <div class="muted small">{d.hostname}</div>}
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
                      <td class="soft">{groupName(d.group, groups) || "-"}</td>
                      <td class="num small">
                        ↓ {rate(d.rate.down)}
                        <br />↑ {rate(d.rate.up)}
                      </td>
                      <td class="num small">
                        ↓ {bytes(d.last24h.down)}
                        <br />↑ {bytes(d.last24h.up)}
                      </td>
                      <td>
                        {d.topApp ? (
                          <span class="app-cell">
                            <AppIcon id={d.topApp.id} name={appName(d.topApp.name)} size={20} />
                            <span>{appName(d.topApp.name)}</span>
                          </span>
                        ) : (
                          "-"
                        )}
                      </td>
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
