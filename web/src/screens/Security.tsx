// Security: the devices in quarantine first (approve, with a group), then the
// alerts newest first, then the alert and quarantine switches and how alerts
// reach the owner. Each control says what it does beside it.

import { h } from "preact";
import { useState } from "preact/hooks";
import { getJSON, mac as enc, sendJSON, usePoll } from "../api";
import { Header, Status, Loading, Empty, useSession } from "../ui";
import { alertText, alertsBody, pendingText, quarantineText, telegramText, type AlertsState } from "../security";
import type { Device } from "../types";
import { deviceName } from "./Clients";

interface Group { id: string; name: string; builtin?: boolean }

export function Security() {
  const { canWrite } = useSession();
  const [n, setN] = useState(0);
  const refresh = () => setN((x) => x + 1);
  const st = usePoll((s) => getJSON<AlertsState>("/api/alerts", s), 30_000, [n]);
  const devices = usePoll((s) => getJSON<Device[]>("/api/devices", s), 30_000, [n]);
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 0, [n]);
  const [pick, setPick] = useState<Record<string, string>>({});
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const save = async (fn: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setErr("");
    try {
      await fn();
      refresh();
    } catch (x) {
      setErr(x instanceof Error ? x.message : String(x));
    } finally {
      setBusy(false);
    }
  };
  const all = devices.data ?? [];
  const nameOf = (mac: string) => {
    const d = all.find((x) => x.mac === mac);
    return d ? deviceName(d) : "";
  };
  const held = all.filter((d) => d.quarantined);
  const s = st.data;
  const toggle = (label: string, on: boolean, change: () => void, note?: string) => (
    <label class="check">
      <input type="checkbox" checked={on} disabled={!canWrite || busy} onChange={change} />
      <span>{label}</span>
      {note && <span class="small muted"> {note}</span>}
    </label>
  );
  const setAlerts = (o: Partial<AlertsState["settings"]>) =>
    s && save(() => sendJSON("PUT", "/api/alerts", alertsBody({ ...s.settings, ...o })));

  return (
    <div class="page">
      <Header section="security" />
      <main class="main">
        <div class="toolbar">
          <h1>Security</h1>
          <span class="muted small">quarantine of new devices and alerts</span>
        </div>
        <Status polled={st} />
        {err && <p class="banner" role="alert">{err}</p>}
        {!s ? (st.error ? null : <Loading />) : (
          <div class="stack">
            <section class="card">
              <h2>New devices in quarantine</h2>
              <p class={s.quarantineState?.active ? "small" : "small muted"}>{quarantineText(s)}</p>
              <p class="small muted">Only the internet and the router's DNS and DHCP, until you approve them.</p>
              {held.length === 0 ? <Empty>No new devices.</Empty> : held.map((d) => (
                <div key={d.mac} class="row-wrap small-13">
                  <a class="break" href={`#/client/${enc(d.mac)}`}>{pendingText({ mac: d.mac, hostname: d.hostname, likelySame: d.likelySame }, nameOf)}</a>
                  {canWrite && (
                    <span class="row-wrap">
                      <select aria-label="Group" value={pick[d.mac] ?? ""} disabled={busy}
                        onChange={(e) => setPick({ ...pick, [d.mac]: (e.target as HTMLSelectElement).value })}>
                        <option value="">no group</option>
                        {(groups.data ?? []).filter((g) => !g.builtin).map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
                      </select>
                      <button type="button" class="btn" disabled={busy}
                        onClick={() => save(() => sendJSON("PUT", `/api/devices/${enc(d.mac)}`, pick[d.mac] ? { approved: true, group: pick[d.mac] } : { approved: true }))}>
                        Approve
                      </button>
                    </span>
                  )}
                </div>
              ))}
            </section>
            <section class="card">
              <h2>Alerts</h2>
              <p class={s.telegram.ready ? "small" : "small muted"}>{telegramText(s)}</p>
              {!s.countryTriesSeen && <p class="small muted">Attempts to reach blocked countries cannot be seen: handshake capture is off.</p>}
              {s.alerts.length === 0 ? <Empty>No alerts yet.</Empty> : s.alerts.map((a) => (
                <div key={a.id} class="col-tight small-13">
                  <span class="break">{alertText(a, nameOf)}</span>
                  {!a.sent && a.problem && <span class="soft small">not sent: {a.problem}</span>}
                </div>
              ))}
            </section>
            <section class="card">
              <h2>What to alert on</h2>
              <div class="checks">
                {toggle("New device", s.settings.newDevice, () => setAlerts({ newDevice: !s.settings.newDevice }))}
                {toggle("Attempt to reach a blocked country", s.settings.blockedCountry, () => setAlerts({ blockedCountry: !s.settings.blockedCountry }), "once a day per device and country")}
                {toggle("Unusual upload", s.settings.upload, () => setAlerts({ upload: !s.settings.upload }), "four times the week's maximum and at least 200 MB in an hour")}
                {toggle("Console only, no Telegram", s.settings.channel === "console", () => setAlerts({ channel: s.settings.channel === "console" ? "telegram" : "console" }))}
                {toggle("Quarantine new devices", s.settings.quarantine, () => save(() => sendJSON("PUT", "/api/quarantine", { enabled: !s.settings.quarantine })), "with quarantine off, new devices reach the house at once")}
              </div>
            </section>
          </div>
        )}
      </main>
    </div>
  );
}
