// Destinations: the countries, networks and domains the house reached in the
// period, who went there and which rules close them; and one country: its
// clients, apps and networks over the period.

import { ClientLink } from "./entity";
import { h, type ComponentChildren } from "preact";
import { useRef } from "preact/hooks";
import { SortTh, useFlip, useSteadyRows } from "./table";
import type { CountryDetail, HouseDestinations, Range } from "../types";
import { getJSON, usePoll } from "../api";
import { RANGES, windowLabel, type Win } from "../query";
import { Bars } from "../charts";
import { Header, Card, CC, Status, Loading, Empty, AppIcon } from "../ui";
import { bytes, rate, share, num, appName, countryName, clientName, plural } from "../format";
import { clientsLine, closesHouse, countryHref, domainText, rulesForCountry } from "../house";
import { BlockCountry, CDN_NOTE, RuleTags, useRuleNames, useRules } from "./controls";
import { HouseTraffic } from "./Apps";

const periodOf = (r: Range) => RANGES.find((x) => x.id === r)?.label ?? r;

export function Destinations({ range, onRange, win, onWin }: { range: Range; onRange: (r: Range) => void; win: Win; onWin: (w: Win) => void }) {
  const polled = usePoll((s) => getJSON<HouseDestinations>(`/api/destinations?range=${range}&window=${win}`, s), 15_000, [range, win]);
  const { rules } = useRules();
  const names = useRuleNames();
  const d = polled.data;
  const { rows: countries, sort, pick } = useSteadyRows(
    "destinations",
    d?.countries ?? [],
    (c) => c.cc,
    {
      name: (c) => countryName(c.cc),
      clients: (c) => c.clients,
      share: (c) => c.share,
      bytes: (c) => c.down + c.up,
      now: (c) => (c.recent ? c.recent.down + c.recent.up : 0),
    },
    { col: "bytes", dir: "desc" },
  );
  const body = useRef<HTMLTableSectionElement>(null);
  useFlip(body);
  const th = (label: ComponentChildren, col: string, first: "asc" | "desc" = "desc", num = false) => <SortTh label={label} col={col} sort={sort} pick={pick} first={first} num={num} />;
  return (
    <div class="page">
      <Header section="destinations" range={range} onRange={onRange} win={win} onWin={onWin} />
      <main class="main">
        <Status polled={polled} />
        <div class="toolbar">
          <h1>Destinations</h1>
          {d && (
            <span class="muted small">
              {plural(d.countries.filter((c) => !c.other).length, "country", "countries")} · {plural(d.networks.length, "network", "networks")} over {periodOf(range)}; the house's own traffic is left out of the countries and networks
            </span>
          )}
        </div>
        {!d ? (
          <Loading />
        ) : (
          <div class="stack">
            <section aria-label="Countries table" class="card card-flush">
              <div class="scroll-x">
                <table class="table house-table">
                  <thead>
                    <tr>
                      {th("Country", "name", "asc")}
                      {th("Clients", "clients")}
                      {th("Share", "share")}
                      {th(`${periodOf(range)} ↓ · ↑`, "bytes", "desc", true)}
                      {th(`Now, ${windowLabel(win)} avg`, "now", "desc", true)}
                      <th>Rules</th>
                    </tr>
                  </thead>
                  <tbody ref={body}>
                    {countries.map((c) => {
                      const href = countryHref(c.cc);
                      const name = c.other || c.cc === "--" ? "unknown or others" : countryName(c.cc);
                      const closing = rulesForCountry(rules, c.cc);
                      return (
                        <tr key={c.cc} data-key={c.cc}>
                          <td>
                            <span class="app-cell">
                              <CC cc={c.cc} />
                              {href ? <a href={href} class="strong">{name}</a> : <span class="strong">{name}</span>}
                            </span>
                          </td>
                          <td class="small">
                            {c.clients > 0 && <span class="strong">{num(c.clients)} </span>}<span class="soft">{clientsLine(c.clients, c.top)}</span>
                          </td>
                          <td class="share-cell">
                            <Bars thin parts={[{ pct: c.share * 100, kind: "down" }]} />
                            <span class="muted small">{share(c.share)}</span>
                          </td>
                          <td class="num small">
                            ↓ {bytes(c.down)}
                            <br />{" "}↑ {bytes(c.up)}
                          </td>
                          <td class="num small">
                            ↓ {rate(c.recent?.down ?? 0)}
                            <br />{" "}↑ {rate(c.recent?.up ?? 0)}
                          </td>
                          <td class="small">
                            <RuleTags rules={closing} names={names} />
                            {href && !closesHouse(closing, Date.now()) && <BlockCountry cc={c.cc} />}
                          </td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
              {d.countries.length === 0 && <Empty>No traffic left the house in this period yet.</Empty>}
            </section>
            <div class="grid-2">
              <Card label="Networks" title="Networks" aside={<span class="muted small">the organisation behind each address, by country</span>}>
                {d.networks.length === 0 && <Empty>No networks in this period.</Empty>}
                {d.networks.map((n) => (
                  <a key={n.cc + n.asn} href={countryHref(n.cc)} class="country-row row-link">
                    <CC cc={n.cc} />
                    <div class="col-tight">
                      <span>{n.other ? "other networks" : n.org || "no organisation"}</span>
                      <span class="muted small">{countryName(n.cc)}{n.asn && n.asn !== "0" ? ` · AS${n.asn}` : ""}</span>
                    </div>
                    <span class="soft small right nowrap">↓ {bytes(n.down)}<br />↑ {bytes(n.up)}</span>
                  </a>
                ))}
              </Card>
              <Card label="Domains" title="Domains" aside={<span class="muted small">the names the house's apps asked for</span>}>
                {d.domains.length === 0 && <Empty>No names in this period.</Empty>}
                {d.domains.map((dm) => (
                  <div key={(dm.app?.id ?? "") + "|" + dm.name} class="app-row">
                    <AppIcon id={dm.app?.id ?? "unknown"} name={appName(dm.app?.name ?? "")} size={24} />
                    <div class="col-tight">
                      <span class="mono small break">{domainText(dm)}</span>
                      <span class="muted small">{dm.app ? appName(dm.app.name) : "app unknown"}</span>
                    </div>
                    <span class="soft small right nowrap">↓ {bytes(dm.down)}<br />↑ {bytes(dm.up)}</span>
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

export function Destination({ cc, range, onRange }: { cc: string; range: Range; onRange: (r: Range) => void }) {
  const polled = usePoll((s) => getJSON<CountryDetail>(`/api/destinations/${cc}?range=${range}`, s), 15_000, [cc, range]);
  const { rules } = useRules();
  const names = useRuleNames();
  const d = polled.data;
  const closing = rulesForCountry(rules, cc);
  return (
    <div class="page">
      <Header section="destinations" range={range} onRange={onRange} />
      <main class="main">
        <Status polled={polled} />
        <p class="crumbs"><a href="#/destinations">Destinations</a> / {countryName(cc)}</p>
        {!d ? (
          <Loading />
        ) : (
          <div class="stack">
            <Card label="Country">
              <div class="head-row">
                <CC cc={d.cc} />
                <div class="col-tight grow">
                  <h1>{countryName(d.cc)}</h1>
                  <span class="soft small">
                    {plural(d.clientCount, "client", "clients")} · {share(d.share)} of what left the house · {plural(d.networks.length, "network", "networks")}
                  </span>
                  <RuleTags rules={closing} names={names} />
                </div>
                <span class="soft small right">
                  ↓ {bytes(d.down)}
                  <br />↑ {bytes(d.up)}
                </span>
              </div>
              {!closesHouse(closing, Date.now()) ? <BlockCountry cc={d.cc} /> : <span class="muted small">The country is told by the address: {CDN_NOTE}.</span>}
            </Card>
            <HouseTraffic d={d} range={range} />
            <div class="grid-3">
              <Card label="Clients that went there" title="Clients">
                {d.clients.length === 0 && <Empty>No client went there in this period.</Empty>}
                {d.clients.map((c) => (
                  <div key={c.mac} class="row-link">
                    <div class="row-between">
                      <span class="strong">{c.other ? (c.mac === "other" ? "other devices" : clientName(c)) : <ClientLink mac={c.mac} name={clientName(c)} />}</span>
                      <span class="soft small nowrap">↓ {bytes(c.down)} · ↑ {bytes(c.up)}</span>
                    </div>
                    {c.other && c.mac !== "other" && <div class="muted small">{c.mac === "router" ? "the router's own traffic" : "no hardware address"}</div>}
                    <Bars thin parts={[{ pct: d.down + d.up ? (100 * (c.down + c.up)) / (d.down + d.up) : 0, kind: "down" }]} />
                  </div>
                ))}
              </Card>
              <Card label="Apps that went there" title="Apps">
                {d.apps.length === 0 && <Empty>No apps in this period.</Empty>}
                {d.apps.map((a) => {
                  const name = a.other ? "other apps" : appName(a.app.name);
                  return (
                    <a key={a.app.id} href={a.other ? undefined : `#/app/${encodeURIComponent(a.app.id)}`} class="app-row row-link">
                      <AppIcon id={a.other ? "other" : a.app.id} name={name} size={28} />
                      <span class="strong ellipsis">{name}</span>
                      <span class="soft small right nowrap">↓ {bytes(a.down)}<br />↑ {bytes(a.up)}</span>
                    </a>
                  );
                })}
              </Card>
              <Card label="Networks in the country" title="Networks">
                {d.networks.length === 0 && <Empty>No networks in this period.</Empty>}
                {d.networks.map((n) => (
                  <div key={n.asn} class="row-between domain-row">
                    <div class="col-tight">
                      <span>{n.other ? "other networks" : n.org || "no organisation"}</span>
                      {n.asn && n.asn !== "0" && <span class="muted small">AS{n.asn}</span>}
                    </div>
                    <span class="soft small right nowrap">↓ {bytes(n.down)}<br />↑ {bytes(n.up)}</span>
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
