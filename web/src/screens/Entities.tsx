// The pages a name leads to that have no screen of their own elsewhere: one
// network (AS), one domain and one group, across the house.

import { h, Fragment } from "preact";
import type { Device, DomainPage, Group, NetworkPage, Range } from "../types";
import { getJSON, usePoll } from "../api";
import { RANGES } from "../query";
import { Header, Card, CC, Status, Loading, Empty, AppIcon, IconContext, usePeriod } from "../ui";
import { Bars } from "../charts";
import { appName, bytes, countryName, groupName, nameSource, plural } from "../format";
import { parentsOf } from "../entity";
import { describeRule, manifestName, stateOf } from "../rules";
import { useContext } from "preact/hooks";
import { deviceName } from "./Clients";
import { ChipChain, ClientLink, EntityLink } from "./entity";

const DEST = [{ label: "Destinations", href: "#/destinations" }];
import { useRules } from "./controls";

const periodOf = (r: Range) => RANGES.find((x) => x.id === r)?.label ?? r;
const overOf = (r: Range, span: boolean) => (span ? "the stretch" : periodOf(r));


function ClientRows({ clients, total, from }: { clients: { mac: string; name: string; down: number; up: number }[]; total: number; from: string }) {
  if (clients.length === 0) return <Empty>No client in this period.</Empty>;
  return (
    <>
      {clients.map((c) => (
        <div key={c.mac} class="row-item">
          <div class="row-between">
            <ClientLink mac={c.mac} name={c.name} from={from} class="strong" />
            <span class="soft small nowrap">↓ {bytes(c.down)} · ↑ {bytes(c.up)}</span>
          </div>
          <Bars thin parts={[{ pct: total ? (100 * (c.down + c.up)) / total : 0, kind: "down" }]} />
        </div>
      ))}
    </>
  );
}

export function NetworkScreen({ asn, from, range, onRange }: { asn: number; from: string; range: Range; onRange: (r: Range) => void }) {
  const per = usePeriod(range);
  const polled = usePoll((s) => getJSON<NetworkPage>(`/api/networks/${asn}?${per.q}`, s), 15_000, [asn, per.q]);
  const d = polled.data;
  const total = d ? d.down + d.up : 0;
  return (
    <div class="page">
      <Header section="destinations" range={range} onRange={onRange} />
      <main class="main">
        <Status polled={polled} />
        <ChipChain from={from} base={DEST} last={d?.org || `AS${asn}`} parents={parentsOf({ kind: "network", asn }, d)} />
        {!d ? (
          <Loading />
        ) : (
          <div class="stack">
            <Card label="Network">
              <div class="card-head">
                <h1>{d.org || "no organisation"}</h1>
                <span class="soft small">
                  AS{d.asn} · ↓ {bytes(d.down)} · ↑ {bytes(d.up)} over {overOf(range, !!per.span)}
                </span>
              </div>
              <a class="btn self-start" href={`#/flows?q=&asn=${asn}`}>Show its flows</a>
            </Card>
            <div class="grid-3">
              <Card label="Countries of the network" title="Countries">
                {d.countries.length === 0 && <Empty>Not reached in this period.</Empty>}
                {d.countries.map((c) => (
                  <div key={c.cc} class="country-row">
                    <CC cc={c.cc} />
                    <EntityLink e={{ kind: "country", cc: c.cc }}>{countryName(c.cc)}</EntityLink>
                    <span class="soft small right nowrap">↓ {bytes(c.down)}<br />↑ {bytes(c.up)}</span>
                  </div>
                ))}
              </Card>
              <Card label="Clients that reached it" title="Clients">
                <ClientRows clients={d.clients} total={total} from={`asn:${asn}`} />
              </Card>
              <Card label="Apps that reached it" title="Apps">
                {d.apps.length === 0 && <Empty>No apps in this period.</Empty>}
                {d.apps.map((a) => (
                  <div key={a.app.id} class="app-row">
                    <AppIcon id={a.other ? "other" : a.app.id} name={appName(a.app.name)} size={24} />
                    <EntityLink e={{ kind: "app", id: a.other ? "other" : a.app.id }} class="ellipsis">{a.other ? "other apps" : appName(a.app.name)}</EntityLink>
                    <span class="soft small right nowrap">↓ {bytes(a.down)}</span>
                  </div>
                ))}
              </Card>
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

export function DomainScreen({ domain, from, range, onRange }: { domain: string; from: string; range: Range; onRange: (r: Range) => void }) {
  const per = usePeriod(range);
  const polled = usePoll((s) => getJSON<DomainPage>(`/api/domains/${encodeURIComponent(domain)}?${per.q}`, s), 15_000, [domain, per.q]);
  const d = polled.data;
  return (
    <div class="page">
      <Header section="destinations" range={range} onRange={onRange} />
      <main class="main">
        <Status polled={polled} />
        <ChipChain from={from} base={DEST} last={domain} parents={parentsOf({ kind: "domain", name: domain }, d)} />
        {!d ? (
          <Loading />
        ) : (
          <div class="stack">
            <Card label="Domain">
              <div class="card-head">
                <h1 class="mono break">{d.name}</h1>
                <span class="soft small">↓ {bytes(d.down)} · ↑ {bytes(d.up)} over {overOf(range, !!per.span)}</span>
              </div>
              <a class="btn self-start" href={`#/flows?q=${encodeURIComponent(d.name)}`}>Show its flows</a>
            </Card>
            <div class="grid-2">
              <Card label="Apps it was named for" title="Apps">
                {d.apps.length === 0 && <Empty>No app named it in this period.</Empty>}
                {d.apps.map((a) => (
                  <div key={a.app.id} class="app-row">
                    <AppIcon id={a.app.id} name={appName(a.app.name)} size={24} />
                    <div class="col-tight">
                      <EntityLink e={{ kind: "app", id: a.app.id }}>{appName(a.app.name)}</EntityLink>
                      <span class="muted small">{nameSource(a.src)}</span>
                    </div>
                    <span class="soft small right nowrap">↓ {bytes(a.down)}<br />↑ {bytes(a.up)}</span>
                  </div>
                ))}
              </Card>
              <Card label="Clients that asked for it" title="Clients">
                <ClientRows clients={d.clients} total={d.down + d.up} from={`domain:${d.name}`} />
              </Card>
            </div>
          </div>
        )}
      </main>
    </div>
  );
}

export function GroupScreen({ id, from = "" }: { id: string; from?: string }) {
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 30_000, []);
  const devices = usePoll((s) => getJSON<Device[]>("/api/devices", s), 15_000, []);
  const { rules } = useRules();
  const appNames = manifestName(useContext(IconContext));
  const g = groups.data?.find((x) => x.id === id);
  const members = (devices.data ?? []).filter((d) => d.group === id || g?.members.includes(d.mac));
  const now = Date.now();
  // The group's own rules, and everyone's: what reaches each of its members.
  const onGroup = (rules ?? []).filter((r) => (r.who === "group" && r.group === id) || r.who === "all");
  return (
    <div class="page">
      <Header section="clients" />
      <main class="main">
        <Status polled={groups} />
        <ChipChain from={from} base={[{ label: "Clients", href: "#/clients" }]} last={g?.name ?? id} />
        {!groups.data ? (
          <Loading />
        ) : !g ? (
          <Empty>The router knows no group {id}.</Empty>
        ) : (
          <div class="grid-2">
            <Card label="Members" title={`${g.name} · ${plural(members.length, "device", "devices")}`}>
              {members.length === 0 && <Empty>No device in this group.</Empty>}
              {members.map((d) => (
                <div key={d.mac} class="row-between row-item">
                  <span>
                    <span class={d.online ? "dot dot-on" : "dot dot-off"} aria-hidden="true" />{" "}
                    <ClientLink mac={d.mac} name={deviceName(d)} from={`group:${id}`} />
                  </span>
                  <span class="soft small nowrap">↓ {bytes(d.last24h.down)} in 24 h</span>
                </div>
              ))}
            </Card>
            <Card label="Rules on the group" title="Rules" aside={<a class="small" href="#/rules">all rules</a>}>
              {!rules ? (
                <Loading />
              ) : onGroup.length === 0 ? (
                <Empty>No rule reaches this group.</Empty>
              ) : (
                onGroup.map((r) => (
                  <div key={r.id} class="col-tight small-13 rule-on">
                    <EntityLink e={{ kind: "rule", id: r.id }}>{describeRule(r, { app: appNames, group: (x) => groupName(x, groups.data ?? []) })}</EntityLink>
                    <span class="muted small">{r.who === "all" ? "for everyone" : "for this group"} · blocked {r.hits} {r.hits === 1 ? "time" : "times"}</span>
                    <span class={`state state-${stateOf(r, now).kind}`}><span class="dot" aria-hidden="true" />{stateOf(r, now).text}</span>
                  </div>
                ))
              )}
            </Card>
          </div>
        )}
      </main>
    </div>
  );
}
