// The house's people (#/people) and one person (#/person/<id>): who they
// are and which devices are theirs. A person changes no rule; it says whose
// a device is. Only a session that may write sees the controls.

import { h } from "preact";
import { useState } from "preact/hooks";
import type { Device, Person } from "../types";
import { getJSON, sendJSON, usePoll } from "../api";
import { Header, Card, Status, Loading, Empty, useSession } from "../ui";
import { HourSparkline } from "../charts";
import { bytes, plural, rate } from "../format";
import { personId } from "../people";
import { ChipChain, ClientLink } from "./entity";
import { deviceName } from "./Clients";

const PEOPLE = [{ label: "Clients", href: "#/clients" }];

function usePeople() {
  const [n, setN] = useState(0);
  const p = usePoll((s) => getJSON<Person[]>("/api/people", s), 30_000, [n]);
  return { polled: p, refresh: () => setN((x) => x + 1) };
}

export function PeopleScreen() {
  const { polled, refresh } = usePeople();
  const devices = usePoll((s) => getJSON<Device[]>("/api/devices", s), 30_000, []).data ?? [];
  const { canWrite } = useSession();
  const [name, setName] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const people = polled.data ?? [];
  const byMac = new Map(devices.map((d) => [d.mac, d]));
  const create = async () => {
    const n = name.trim();
    if (!n || busy) return;
    setBusy(true);
    setErr("");
    try {
      const id = personId(n, people.map((p) => p.id));
      await sendJSON("PUT", `/api/people/${id}`, { name: n, macs: [] });
      setName("");
      refresh();
      location.hash = `#/person/${id}`;
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div class="page">
      <Header section="clients" />
      <main class="main">
        <Status polled={polled} />
        <ChipChain base={PEOPLE} last="People" />
        <div class="toolbar">
          <h1>People</h1>
          <span class="muted small">whose each device is; a person changes no rule</span>
        </div>
        {canWrite && (
          <form class="toolbar" onSubmit={(e) => { e.preventDefault(); void create(); }}>
            <div class="field grow">
              <label for="person-name" class="muted small">New person</label>
              <input id="person-name" class="input" value={name} maxLength={64} placeholder="a name" onInput={(e) => setName((e.target as HTMLInputElement).value)} />
            </div>
            <button type="submit" class="btn" disabled={busy || !name.trim()}>Add</button>
            {err && <span class="bad-text small">{err}</span>}
          </form>
        )}
        {!polled.data ? (
          <Loading />
        ) : people.length === 0 ? (
          <Empty>No people yet{canWrite ? ": add one, then give them their devices." : "."}</Empty>
        ) : (
          <div class="grid-3">
            {people.map((p) => (
              <Card key={p.id} label={`Person ${p.name}`}>
                <a class="strong" href={`#/person/${p.id}`}>{p.name}</a>
                <span class="muted small">{plural(p.macs.length, "device", "devices")}</span>
                {p.macs.map((m) => {
                  const d = byMac.get(m);
                  return <div key={m} class="small">{d ? <ClientLink mac={m} name={deviceName(d)} from="" /> : m}</div>;
                })}
              </Card>
            ))}
          </div>
        )}
      </main>
    </div>
  );
}

export function PersonScreen({ id }: { id: string }) {
  const { polled, refresh } = usePeople();
  const devs = usePoll((s) => getJSON<Device[]>("/api/devices", s), 10_000, []);
  const { canWrite } = useSession();
  const p = (polled.data ?? []).find((x) => x.id === id);
  const devices = devs.data ?? [];
  const mine = p ? devices.filter((d) => p.macs.includes(d.mac)) : [];
  const total = mine.reduce((a, d) => ({ down: a.down + d.last24h.down, up: a.up + d.last24h.up }), { down: 0, up: 0 });
  return (
    <div class="page">
      <Header section="clients" />
      <main class="main">
        <Status polled={polled} />
        <ChipChain base={[...PEOPLE, { label: "People", href: "#/people" }]} last={p?.name ?? id} />
        {!polled.data ? (
          <Loading />
        ) : !p ? (
          <Empty>The router knows no person {id}.</Empty>
        ) : (
          <div class="stack">
            <Card label="Person">
              <h1>{p.name}</h1>
              <span class="soft small">
                {plural(p.macs.length, "device", "devices")} · 24 h ↓ {bytes(total.down)} · ↑ {bytes(total.up)}
              </span>
            </Card>
            <Card label="Their devices" title="Their devices">
              {mine.length === 0 && <Empty>No device is theirs yet.</Empty>}
              {mine.map((d) => (
                <div key={d.mac} class="row-between domain-row">
                  <span>
                    <span class={d.online ? "dot dot-on" : "dot dot-off"} aria-hidden="true" /> <ClientLink mac={d.mac} name={deviceName(d)} from="" class="strong" />
                  </span>
                  <HourSparkline s={d.spark} />
                  <span class="soft small nowrap">now ↓ {rate(d.recent?.down ?? 0)} · 24 h ↓ {bytes(d.last24h.down)}</span>
                </div>
              ))}
            </Card>
            {canWrite && <PersonEdit p={p} people={polled.data ?? []} devices={devices} done={refresh} />}
          </div>
        )}
      </main>
    </div>
  );
}

function PersonEdit({ p, people, devices, done }: { p: Person; people: Person[]; devices: Device[]; done: () => void }) {
  const [name, setName] = useState(p.name);
  const [macs, setMacs] = useState<string[]>(p.macs);
  const [err, setErr] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [sure, setSure] = useState(false);
  const owner = (m: string) => people.find((x) => x.id !== p.id && x.macs.includes(m));
  const run = async (fn: () => Promise<unknown>, ok: string) => {
    setBusy(true);
    setErr("");
    setNote("");
    try {
      await fn();
      setNote(ok);
      done();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card label="Change the person" title="Change">
      <div class="field">
        <label for="person-edit-name" class="muted small">Name</label>
        <input id="person-edit-name" class="input" value={name} maxLength={64} onInput={(e) => setName((e.target as HTMLInputElement).value)} />
      </div>
      <span class="muted small">Their devices: a device is one person's at most</span>
      <div class="checks">
        {devices.map((d) => {
          const o = owner(d.mac);
          return (
            <label key={d.mac} class="check">
              <input
                type="checkbox"
                checked={macs.includes(d.mac)}
                disabled={busy || !!o}
                onChange={() => setMacs((s) => (s.includes(d.mac) ? s.filter((m) => m !== d.mac) : [...s, d.mac]))}
              />
              <span>
                {deviceName(d)}
                {o && <span class="muted small"> · {o.name}'s</span>}
              </span>
            </label>
          );
        })}
      </div>
      <div class="row-wrap">
        <button type="button" class="btn" disabled={busy || !name.trim()} onClick={() => run(() => sendJSON("PUT", `/api/people/${p.id}`, { name: name.trim(), macs }), "Saved.")}>
          Save
        </button>
        {!sure ? (
          <button type="button" class="btn-mini" disabled={busy} onClick={() => setSure(true)}>Delete person</button>
        ) : (
          <button
            type="button"
            class="btn-mini btn-mini-danger"
            disabled={busy}
            onClick={() => run(async () => { await sendJSON("DELETE", `/api/people/${p.id}`); location.hash = "#/people"; }, "Deleted.")}
          >
            Delete {p.name}: the devices and rules stay
          </button>
        )}
        {note && <span class="small">{note}</span>}
        {err && <span class="bad-text small">{err}</span>}
      </div>
    </Card>
  );
}
