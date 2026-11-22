// Apps: every app the house used in the period, and one app across the
// house: who used it, where it went, how it was named, when it moved.

import { filterHash } from "../entity";
import { EntityLink, ClientNames } from "./entity";
import { ClientLink } from "./entity";
import { h, type ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { SortTh, useFlip, useSteadyRows } from "./table";
import type { AppDetail, HouseApp, Range } from "../types";
import { getJSON, usePoll } from "../api";
import { RANGES, stepOf, windowLabel, type Win } from "../query";
import { splitSeries, timeTicks } from "../geometry";
import { AreaChart, Bars, HourSparkline, Legend } from "../charts";
import { Header, Pills, Card, CC, Status, Loading, Empty, AppIcon } from "../ui";
import { bytes, rate, share, num, appName, category, countryName, clientName, nameSource, plural } from "../format";
import { appHref, appsSummary, categoriesIn, closesHouse, domainText, matchApp, rulesForApp } from "../house";
import { BlockApp, RuleTags, useRuleNames, useRules } from "./controls";
import { LAYER_COLORS, UP_COLOR } from "../view";

const periodOf = (r: Range) => RANGES.find((x) => x.id === r)?.label ?? r;

export function Apps({ cat0, q0, range, onRange, win, onWin }: { cat0: string; q0: string; range: Range; onRange: (r: Range) => void; win: Win; onWin: (w: Win) => void }) {
  const polled = usePoll((s) => getJSON<HouseApp[]>(`/api/apps?range=${range}&window=${win}`, s), 15_000, [range, win]);
  const { rules } = useRules();
  const names = useRuleNames();
  const [q, setQ] = useState(q0);
  const [cat] = useState(cat0);
  // The search and the category are kept in the address, so Back and a
  // shared link keep them; a category is a step of its own in the history.
  useEffect(() => {
    const h = filterHash("apps", { cat, q });
    if (location.hash !== h) history.replaceState(null, "", h);
  }, [q]);
  const pickCat = (c: string) => {
    location.hash = filterHash("apps", { cat: c, q });
  };
  const all = polled.data ?? [];
  const cats = categoriesIn(all);
  const found = all.filter((a) => (!cat || a.app.category === cat) && matchApp(a, q));
  const { rows, sort, pick, holdProps } = useSteadyRows(
    "apps",
    found,
    (a) => a.app.id,
    {
      name: (a) => appName(a.app.name),
      category: (a) => category(a.app.category),
      clients: (a) => a.clients,
      share: (a) => a.share,
      bytes: (a) => a.down + a.up,
      now: (a) => (a.recent ? a.recent.down + a.recent.up : 0),
    },
    { col: "bytes", dir: "desc" },
  );
  const body = useRef<HTMLTableSectionElement>(null);
  useFlip(body);
  const th = (label: ComponentChildren, col: string, first: "asc" | "desc" = "desc", num = false, also?: string[]) => (
    <SortTh label={label} col={col} sort={sort} pick={pick} first={first} num={num} also={also} />
  );
  const down = all.reduce((s, a) => s + a.down, 0);
  const up = all.reduce((s, a) => s + a.up, 0);

  return (
    <div class="page">
      <Header section="apps" range={range} onRange={onRange} win={win} onWin={onWin} />
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
          <Pills label="Category" value={cat} onPick={pickCat} items={[{ id: "", label: "All" }, ...cats.map((c) => ({ id: c, label: category(c) }))]} />
        )}
        {!polled.data ? (
          <Loading />
        ) : (
          <section aria-label="Apps table" class="card card-flush">
            <div class="scroll-x">
              <table class="table house-table">
                <thead>
                  <tr>
                    {th("App", "name", "asc")}
                    {th("Category", "category", "asc")}
                    {th("Clients", "clients")}
                    {th("Share", "share")}
                    {th(`${periodOf(range)} ↓ · ↑`, "bytes", "desc", true)}
                    {th(`Now, ${windowLabel(win)} avg`, "now", "desc", true)}
                    <th>Last hour</th>
                    <th>Rules</th>
                  </tr>
                </thead>
                <tbody ref={body} {...holdProps}>
                  {rows.map((a) => {
                    const name = a.other ? "other apps" : appName(a.app.name);
                    const href = appHref(a);
                    return (
                      <tr key={a.app.id} data-key={a.app.id}>
                        <td>
                          <span class="app-cell">
                            <AppIcon id={a.other ? "other" : a.app.id} name={name} size={24} />
                            {href ? <a href={href} class="strong">{name}</a> : <span class="strong">{name}</span>}
                          </span>
                        </td>
                        <td class="soft">{a.other ? "folded rows" : <EntityLink e={{ kind: "category", id: a.app.category }}>{category(a.app.category) || "-"}</EntityLink>}</td>
                        <td class="small">
                          {a.clients > 0 && <span class="strong">{num(a.clients)} </span>}<ClientNames n={a.clients} top={a.top} />
                        </td>
                        <td class="share-cell">
                          <Bars thin parts={[{ pct: a.share * 100, kind: "down" }]} />
                          <span class="muted small">{share(a.share)}</span>
                        </td>
                        <td class="num small">
                          ↓ {bytes(a.down)}
                          <br />{" "}↑ {bytes(a.up)}
                        </td>
                        <td class="num small">
                          ↓ {rate(a.recent?.down ?? 0)}
                          <br />{" "}↑ {rate(a.recent?.up ?? 0)}
                        </td>
                        <td>{!a.other && <HourSparkline s={a.spark} />}</td>
                        <td class="small">{a.other ? "" : <RuleTags rules={rulesForApp(rules, a.app)} names={names} />}</td>
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
  const names = useRuleNames();
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
                    {d.app.category ? <EntityLink e={{ kind: "category", id: d.app.category }}>{category(d.app.category)}</EntityLink> : "no category"} · {plural(d.clientCount, "client", "clients")} · {share(d.share)} of the house's traffic
                  </span>
                  <RuleTags rules={rulesForApp(rules, d.app)} names={names} />
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
                  <div key={c.mac} class="row-item">
                    <div class="row-between">
                      <span class="strong">{c.other ? (c.mac === "other" ? "other devices" : clientName(c)) : <ClientLink mac={c.mac} name={clientName(c)} from={`app:${d.app.id}`} />}</span>
                      <span class="soft small nowrap">↓ {bytes(c.down)} · ↑ {bytes(c.up)}</span>
                    </div>
                    {c.other && c.mac !== "other" && <div class="muted small">{c.mac === "router" ? "the router's own traffic" : "no hardware address"}</div>}
                    <Bars thin parts={[{ pct: d.down + d.up ? (100 * (c.down + c.up)) / (d.down + d.up) : 0, kind: "down" }]} />
                  </div>
                ))}
              </Card>
              <Card label="Where the app goes" title="Countries and networks">
                {d.places.length === 0 && <Empty>No places in this period.</Empty>}
                {d.places.map((p) => (
                  <div key={p.cc + p.asn} class="country-row">
                    <CC cc={p.cc} />
                    <div class="col-tight">
                      <EntityLink e={{ kind: "country", cc: p.other ? "--" : p.cc }}>{p.other ? "unknown or others" : countryName(p.cc)}</EntityLink>
                      <span class="muted small">
                        <EntityLink e={{ kind: "network", asn: p.asn }} from={`app:${d.app.id}`}>{p.org || "no organisation"}</EntityLink>
                        {p.asn && p.asn !== "0" ? ` · AS${p.asn}` : ""}
                      </span>
                    </div>
                    <span class="soft small right nowrap">↓ {bytes(p.down)}<br />↑ {bytes(p.up)}</span>
                  </div>
                ))}
              </Card>
              <Card label="Domains of the app" title="Domains">
                {d.domains.length === 0 && <Empty>No names in this period.</Empty>}
                {d.domains.map((dm) => (
                  <div key={dm.name} class="row-between domain-row">
                    <div class="col-tight">
                      <EntityLink e={{ kind: "domain", name: dm.other ? "" : dm.name }} from={`app:${d.app.id}`} class="mono small break">{domainText(dm)}</EntityLink>
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
