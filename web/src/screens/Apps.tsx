// Apps: every app the house used in the period, and one app across the
// house: who used it, where it went, how it was named, when it moved.

import { h } from "preact";
import { useState } from "preact/hooks";
import type { AppDetail, HouseApp, Range } from "../types";
import { getJSON, mac, usePoll } from "../api";
import { RANGES, stepOf } from "../query";
import { splitSeries, timeTicks } from "../geometry";
import { AreaChart, Bars, Legend } from "../charts";
import { Header, Pills, Card, CC, Status, Loading, Empty, AppIcon } from "../ui";
import { bytes, share, num, appName, category, countryName, clientName, nameSource, plural } from "../format";
import { appHref, appsSummary, categoriesIn, clientsLine, closesHouse, countryHref, domainText, matchApp, rulesForApp } from "../house";
import { BlockApp, RuleTags, useRules } from "./controls";
import { LAYER_COLORS, UP_COLOR } from "../view";

const periodOf = (r: Range) => RANGES.find((x) => x.id === r)?.label ?? r;

export function Apps({ range, onRange }: { range: Range; onRange: (r: Range) => void }) {
  const polled = usePoll((s) => getJSON<HouseApp[]>(`/api/apps?range=${range}`, s), 15_000, [range]);
  const { rules } = useRules();
  const [q, setQ] = useState("");
  const [cat, setCat] = useState("");
  const all = polled.data ?? [];
  const cats = categoriesIn(all);
  const rows = all.filter((a) => (!cat || a.app.category === cat) && matchApp(a, q));
  const down = all.reduce((s, a) => s + a.down, 0);
  const up = all.reduce((s, a) => s + a.up, 0);

  return (
    <div class="page">
      <Header section="apps" range={range} onRange={onRange} />
      <main class="main">
        <Status polled={polled} />
        <div class="toolbar">
          <h1>Apps</h1>
          {polled.data && (
            <span class="muted small">
              {appsSummary(all.length, 100)} · ↓ {bytes(down)} · ↑ {bytes(up)} over {periodOf(range)}
            </span>
          )}
          <div class="field grow">
            <label for="apps-q" class="muted small">Search</label>
            <input id="apps-q" type="search" class="input" value={q} placeholder="app or category" onInput={(e) => setQ((e.target as HTMLInputElement).value)} />
          </div>
        </div>
        {cats.length > 1 && (
          <Pills label="Category" value={cat} onPick={setCat} items={[{ id: "", label: "All" }, ...cats.map((c) => ({ id: c, label: category(c) }))]} />
        )}
        {!polled.data ? (
          <Loading />
        ) : (
          <section aria-label="Apps table" class="card card-flush">
            <div class="scroll-x">
              <table class="table house-table">
                <thead>
                  <tr>
                    <th>App</th>
                    <th>Category</th>
                    <th>Clients</th>
                    <th>Share</th>
                    <th class="num">↓ · ↑</th>
                    <th>Rules</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((a) => {
                    const name = a.other ? "other apps" : appName(a.app.name);
                    const href = appHref(a);
                    return (
                      <tr key={a.app.id}>
                        <td>
                          <span class="app-cell">
                            <AppIcon id={a.other ? "other" : a.app.id} name={name} size={24} />
                            {href ? <a href={href} class="strong">{name}</a> : <span class="strong">{name}</span>}
                          </span>
                        </td>
                        <td class="soft">{a.other ? "folded rows" : category(a.app.category) || "-"}</td>
                        <td class="small">
                          {a.clients > 0 && <span class="strong">{num(a.clients)} </span>}<span class="soft">{clientsLine(a.clients, a.top)}</span>
                        </td>
                        <td class="share-cell">
                          <Bars thin parts={[{ pct: a.share * 100, kind: "down" }]} />
                          <span class="muted small">{share(a.share)}</span>
                        </td>
                        <td class="num small">
                          ↓ {bytes(a.down)}
                          <br />↑ {bytes(a.up)}
                        </td>
                        <td class="small">{a.other ? "" : <RuleTags rules={rulesForApp(rules, a.app)} />}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            {rows.length === 0 && <Empty>{q || cat ? "Nothing found." : "No apps in this period yet."}</Empty>}
          </section>
        )}
      </main>
    </div>
  );
}

export function AppScreen({ id, range, onRange }: { id: string; range: Range; onRange: (r: Range) => void }) {
  const polled = usePoll((s) => getJSON<AppDetail>(`/api/apps/${encodeURIComponent(id)}?range=${range}`, s), 15_000, [id, range]);
  const { rules } = useRules();
  const d = polled.data;
  return (
    <div class="page">
      <Header section="apps" range={range} onRange={onRange} />
      <main class="main">
        <Status polled={polled} />
        <p class="crumbs"><a href="#/apps">Apps</a> / {d ? appName(d.app.name) : id}</p>
        {!d ? (
          <Loading />
        ) : (
          <div class="stack">
            <Card label="App">
              <div class="head-row">
                <AppIcon id={d.app.id} name={appName(d.app.name)} size={44} />
                <div class="col-tight grow">
                  <h1>{appName(d.app.name)}</h1>
                  <span class="soft small">
                    {category(d.app.category) || "no category"} · {plural(d.clientCount, "client", "clients")} · {share(d.share)} of the house's traffic
                  </span>
                  <RuleTags rules={rulesForApp(rules, d.app)} />
                </div>
                <span class="soft small right">
                  ↓ {bytes(d.down)}
                  <br />↑ {bytes(d.up)}
                </span>
              </div>
              {!closesHouse(rulesForApp(rules, d.app), Date.now()) && <BlockApp appId={d.app.id} name={d.app.name} />}
            </Card>
            <Traffic d={d} range={range} />
            <div class="grid-3">
              <Card label="Clients of the app" title="Clients">
                {d.clients.length === 0 && <Empty>No client used it in this period.</Empty>}
                {d.clients.map((c) => (
                  <a key={c.mac} href={c.other ? undefined : `#/client/${mac(c.mac)}`} class="row-link">
                    <div class="row-between">
                      <span class="strong">{c.mac === "other" ? "other devices" : clientName(c)}</span>
                      <span class="soft small nowrap">↓ {bytes(c.down)} · ↑ {bytes(c.up)}</span>
                    </div>
                    {c.other && c.mac !== "other" && <div class="muted small">{c.mac === "router" ? "the router's own traffic" : "no hardware address"}</div>}
                    <Bars thin parts={[{ pct: d.down + d.up ? (100 * (c.down + c.up)) / (d.down + d.up) : 0, kind: "down" }]} />
                  </a>
                ))}
              </Card>
              <Card label="Where the app goes" title="Countries and networks">
                {d.places.length === 0 && <Empty>No places in this period.</Empty>}
                {d.places.map((p) => (
                  <a key={p.cc + p.asn} href={countryHref(p.cc)} class="country-row row-link">
                    <CC cc={p.cc} />
                    <div class="col-tight">
                      <span>{p.other ? "unknown or others" : countryName(p.cc)}</span>
                      <span class="muted small">{p.org || "no organisation"}{p.asn && p.asn !== "0" ? ` · AS${p.asn}` : ""}</span>
                    </div>
                    <span class="soft small right nowrap">↓ {bytes(p.down)}<br />↑ {bytes(p.up)}</span>
                  </a>
                ))}
              </Card>
              <Card label="Domains of the app" title="Domains">
                {d.domains.length === 0 && <Empty>No names in this period.</Empty>}
                {d.domains.map((dm) => (
                  <div key={dm.name} class="row-between domain-row">
                    <div class="col-tight">
                      <span class="mono small break">{domainText(dm)}</span>
                      {!dm.other && <span class="muted small">{nameSource(dm.src)}</span>}
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

function Traffic({ d, range }: { d: { series: AppDetail["series"] }; range: Range }) {
  const s = splitSeries(d.series, stepOf(range), 1);
  const k = s.keys[0];
  return (
    <Card label="Traffic over the period">
      <div class="card-head">
        <div class="card-titles">
          <h2>Traffic</h2>
          <span class="muted small">above the axis down, below it up, in bit/s, over {periodOf(range)}</span>
        </div>
      </div>
      {d.series.length === 0 || !k ? (
        <Empty>No traffic in this period.</Empty>
      ) : (
        <AreaChart down={s.down[k]} up={s.up} viewH={200} axis={150} downH={140} upH={45} label="Traffic chart for the period" />
      )}
      <Legend items={[{ label: "down", color: LAYER_COLORS[0] }, { label: "up", color: UP_COLOR }]} ticks={timeTicks(s.t, range)} />
    </Card>
  );
}

export { Traffic as HouseTraffic };
