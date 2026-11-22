// Network: how the router is wired, read only: its networks, the bridge's
// ports and who is behind each, Wi-Fi radios and clients with their signal,
// WireGuard peers and port forwards. What the router does not have is said.

import { h, Fragment, type ComponentChildren } from "preact";
import type { NetDevice, NetworkView } from "../types";
import { getJSON, mac, usePoll } from "../api";
import { Header, Card, Status, Loading, Empty } from "../ui";
import { bytes, duration, plural } from "../format";
import { forwardTarget, handshakeText, peerConnected, shortKey, signalText, speedText } from "../network";

export function Network() {
  const polled = usePoll((s) => getJSON<NetworkView>("/api/network", s), 10_000, []);
  const d = polled.data;
  return (
    <div class="page">
      <Header section="network" note="Read only: change these in LuCI" />
      <main class="main">
        <Status polled={polled} />
        <div class="toolbar">
          <h1>Network</h1>
        </div>
        {!d ? (
          <Loading />
        ) : (
          <div class="stack">
            <Interfaces d={d} />
            <Ports d={d} />
            <WiFi d={d} />
            <div class="grid-2">
              <WireGuard d={d} />
              <Forwards d={d} />
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

function Why({ text }: { text: string }) {
  return <p class="empty">{text.charAt(0).toUpperCase() + text.slice(1)}.</p>;
}

function Note({ text }: { text: string }) {
  return text ? <p class="muted small">{text.charAt(0).toUpperCase() + text.slice(1)}.</p> : null;
}

function DeviceLink({ d, children }: { d: NetDevice; children?: ComponentChildren }) {
  return (
    <a href={`#/client/${mac(d.mac)}`} class="net-device">
      <span class={d.online ? "dot dot-on" : "dot dot-off"} aria-hidden="true" />
      <span class="sr">{d.online ? "online" : "offline"}</span>
      <span>{d.name}</span>
      {children}
    </a>
  );
}

function Interfaces({ d }: { d: NetworkView }) {
  return (
    <section aria-label="Networks" class="card card-flush">
      <div class="card-head card-pad">
        <h2>Internet and networks</h2>
        <span class="muted small">traffic counted since the router started</span>
      </div>
      {d.interfacesWhy ? (
        <Why text={d.interfacesWhy} />
      ) : (
        <div class="scroll-x">
          <table class="table net-table">
            <thead>
              <tr>
                <th>Network</th>
                <th>State</th>
                <th>Protocol</th>
                <th>Device</th>
                <th>Addresses</th>
                <th>Gateway</th>
                <th>Up for</th>
                <th class="num">↓ · ↑</th>
              </tr>
            </thead>
            <tbody>
              {d.interfaces.map((i) => (
                <tr key={i.name}>
                  <td>
                    <span class="strong">{i.name}</span>
                    {i.wan && <span class="tag">internet</span>}
                  </td>
                  <td>
                    <span class={i.up ? "dot dot-on" : "dot dot-off"} aria-hidden="true" /> {i.up ? "up" : "down"}
                  </td>
                  <td class="soft">{i.proto || "-"}</td>
                  <td class="mono small">{i.device || "-"}</td>
                  <td class="mono small">
                    {[...(i.ipv4 ?? []), ...(i.ipv6 ?? [])].map((a) => (
                      <div key={a}>{a}</div>
                    ))}
                    {!(i.ipv4?.length || i.ipv6?.length) && "-"}
                  </td>
                  <td class="mono small">{i.gateway || "-"}</td>
                  <td class="soft small">{i.up ? duration(i.uptimeSec) : "-"}</td>
                  <td class="num small">
                    ↓ {bytes(i.rxBytes)}
                    <br />↑ {bytes(i.txBytes)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Ports({ d }: { d: NetworkView }) {
  return (
    <Card label="Ports" title="Ports" aside={<span class="muted small">the house bridge's wired ports and the devices learned behind each</span>}>
      {d.ports.length === 0 && <Empty>This router has no wired port in the house bridge.</Empty>}
      <div class="port-grid">
        {d.ports.map((p) => (
          <div key={p.name} class={p.up ? "port port-up" : "port"}>
            <div class="row-between">
              <span class="strong mono">{p.name}</span>
              <span class={p.up ? "small ok" : "small muted"}>{p.up ? speedText(p.speedMbps) || "link up" : "no link"}</span>
            </div>
            <span class="muted small">
              {p.bridge}
              {p.switch ? " · on the hardware switch" : ""}
            </span>
            {p.devices.length === 0 ? (
              <span class="muted small">{p.up ? "no device learned yet" : "nothing plugged in"}</span>
            ) : (
              <div class="col-tight">
                {p.devices.map((x) => (
                  <DeviceLink key={x.mac} d={x} />
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </Card>
  );
}

function WiFi({ d }: { d: NetworkView }) {
  const w = d.wifi;
  const running = new Set(w.live.map((a) => a.ssid));
  const idle = w.aps.filter((a) => a.disabled || !running.has(a.ssid));
  return (
    <Card label="Wi-Fi" title="Wi-Fi" aside={w.why ? undefined : <span class="muted small">{plural(w.live.reduce((n, a) => n + a.clients.length, 0), "client", "clients")} on {plural(w.live.length, "access point", "access points")}</span>}>
      {w.why ? (
        <Why text={w.why} />
      ) : (
        <Fragment>
          <Note text={w.note} />
          {w.radios.length > 0 && (
            <div class="row-wrap">
              {w.radios.map((r) => (
                <span key={r.name} class={r.disabled ? "radio radio-off" : "radio"}>
                  <span class="strong">{r.band || r.name}</span>
                  <span class="muted small">
                    {r.disabled ? "turned off" : [r.channel && `channel ${r.channel}`, r.width].filter(Boolean).join(" · ")}
                  </span>
                </span>
              ))}
            </div>
          )}
          <div class="grid-3">
            {w.live.map((a) => (
              <div key={a.iface} class="ap">
                <div class="row-between">
                  <span class="strong">{a.ssid || "(hidden)"}</span>
                  <span class="muted small">{[a.band, a.channel ? `ch ${a.channel}` : ""].filter(Boolean).join(" · ")}</span>
                </div>
                <span class="muted small mono">{a.iface}</span>
                {a.clients.length === 0 && <span class="muted small">no clients now</span>}
                {a.clients.map((c) => (
                  <DeviceLink key={c.mac} d={c}>
                    <span class="muted small right">{signalText(c.signal ?? 0)}</span>
                  </DeviceLink>
                ))}
              </div>
            ))}
          </div>
          {idle.length > 0 && (
            <p class="muted small">
              Not running: {idle.map((a) => `${a.ssid || a.name}${a.disabled ? " (turned off)" : ""}`).join(", ")}
            </p>
          )}
        </Fragment>
      )}
    </Card>
  );
}

function WireGuard({ d }: { d: NetworkView }) {
  const w = d.wireguard;
  const now = Date.now();
  return (
    <Card label="WireGuard" title="WireGuard">
      {w.why ? (
        <Why text={w.why} />
      ) : (
        <Fragment>
          <Note text={w.note} />
          {w.interfaces.map((i) => (
            <div key={i.name} class="col-tight wg">
              <div class="row-between">
                <span class="strong mono">{i.name}</span>
                <span class="muted small">
                  {[i.listenPort ? `port ${i.listenPort}` : "", i.addresses.join(", "), i.publicKey ? `key ${shortKey(i.publicKey)}` : ""].filter(Boolean).join(" · ")}
                </span>
              </div>
              {i.peers.length === 0 && <span class="muted small">no peers</span>}
              {i.peers.map((p) => (
                <div key={p.publicKey} class="row-between domain-row">
                  <div class="col-tight">
                    <span>
                      <span class={peerConnected(p.handshake, now) ? "dot dot-on" : "dot dot-off"} aria-hidden="true" /> {p.name || shortKey(p.publicKey)}
                    </span>
                    <span class="muted small mono">{[p.endpoint, (p.allowedIps ?? []).join(", ")].filter(Boolean).join(" · ") || "-"}</span>
                    <span class="muted small">last handshake {handshakeText(p.handshake, now)}</span>
                  </div>
                  <span class="soft small right nowrap">
                    ↓ {bytes(p.rx)}
                    <br />↑ {bytes(p.tx)}
                  </span>
                </div>
              ))}
            </div>
          ))}
        </Fragment>
      )}
    </Card>
  );
}

function Forwards({ d }: { d: NetworkView }) {
  const f = d.forwards;
  return (
    <Card label="Port forwards" title="Port forwards" aside={<span class="muted small">ports on the internet side led to a device at home</span>}>
      {f.why ? (
        <Why text={f.why} />
      ) : (
        f.rules.map((r, i) => (
          <div key={`${r.name}-${i}`} class={r.enabled ? "row-between domain-row" : "row-between domain-row is-off"}>
            <div class="col-tight">
              <span class="strong">{r.name || `forward ${i + 1}`}</span>
              <span class="small">
                {r.proto} {r.port} → {r.device ? <a href={`#/client/${mac(r.device.mac)}`}>{forwardTarget(r)}</a> : forwardTarget(r)}
              </span>
            </div>
            <span class="muted small nowrap">{r.enabled ? `from ${r.from || "wan"}` : "turned off"}</span>
          </div>
        ))
      )}
    </Card>
  );
}
