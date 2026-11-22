// Pieces of the client's full view (Client360.tsx) over a period: its
// traffic, each app it used with that app's domains and countries, and where
// it went.

import { EntityLink } from "./entity";
import { h, type ComponentChildren } from "preact";
import { useRef, useState } from "preact/hooks";
import { SortHead, useFlip, useSteadyRows } from "./table";

import type { AppUsage, Destinations, Device, FlowsPage, Range, SeriesPoint } from "../types";

import { RANGES, windowLabel, type Win } from "../query";
import { markersOn, splitSeries, timeTicks } from "../geometry";
import { TimeChart, type Layer } from "../charts";
import { alertText, type Alert } from "../security";
import { Pills, Card, CC, Empty, AppIcon } from "../ui";
import { bytes, rate, share, num, time, appName, category, countryName, nameSource, plural } from "../format";
import { LAYER_COLORS, OTHER_COLOR, UP_COLOR, isRealCountry } from "../view";

import { BlockApp } from "./controls";


interface ClientData {
  device: Device;
  traffic: SeriesPoint[];
  apps: AppUsage[];
  dest: Destinations;
  flows: FlowsPage;
}

type SplitKind = "app" | "none";

export function Traffic({ d, step, range, span, split, onSplit, alerts = [] }: { d: Pick<ClientData, "traffic" | "apps">; step: number; range: Range; span?: boolean; split: SplitKind; onSplit: (s: SplitKind) => void; alerts?: Alert[] }) {
  const s = splitSeries(d.traffic, step, split === "none" ? 1 : 3, ["other"]);
  const names = new Map(d.apps.map((a) => [a.app.id, appName(a.app.name)]));
  const layers: Layer[] = [];
  const layerKeys: (string | undefined)[] = [];
  if (s.other) {
    layers.push({ label: "others", color: OTHER_COLOR, values: s.otherDown, id: "other" });
    layerKeys.push(undefined);
  }
  const colors = [LAYER_COLORS[0], LAYER_COLORS[2], LAYER_COLORS[1]];
  for (let i = s.keys.length - 1; i >= 0; i--) {
    const k = s.keys[i];
    layers.push({ label: split === "none" ? "down" : names.get(k) ?? appName(k), color: colors[i], values: s.down[k], id: k });
    layerKeys.push(split === "none" ? undefined : k);
  }
  // A legend entry names its app, and leads to it.
  const legend: { label: string; color: string; node?: ComponentChildren; id?: string }[] = layers
    .map((l, i) => ({ label: l.label, color: l.color, id: l.id, node: layerKeys[i] ? <EntityLink e={{ kind: "app", id: layerKeys[i]! }}>{l.label}</EntityLink> : undefined }))
    .reverse();
  legend.push({ label: "up", color: UP_COLOR });
  const period = span ? "the stretch" : RANGES.find((r) => r.id === range)?.label ?? range;
  const marks = markersOn(s.t, step, alerts.map((a) => ({ at: a.at, kind: a.kind, text: alertText(a, () => "this device") })));
  const peak = s.peakDown.rate > 0 ? ` · peak ${rate(s.peakDown.rate)} at ${time(s.peakDown.at)}` : "";

  return (
    <Card label="Client traffic">
      <div class="card-head">
        <div class="card-titles">
          <h2>Client traffic</h2>
          <span class="muted small">
            ↓ {bytes(s.totalDown)} · ↑ {bytes(s.totalUp)} over {period}
            {peak}
          </span>
        </div>
        <Pills label="Split" inset value={split} onPick={(v) => onSplit(v as SplitKind)} items={[{ id: "app", label: "by app" }, { id: "none", label: "total" }]} />
      </div>
      {d.traffic.length === 0 ? (
        <Empty>No traffic in this period.</Empty>
      ) : (
        <TimeChart
          layers={layers} up={s.up} t={s.t} stepSec={step} viewH={220} axis={160} downH={150} upH={55}
          label="Client traffic chart for the period; drag across a stretch to look at it"
          brush spike markers={marks} legend={legend} ticks={timeTicks(s.t, range, !span)}
        />
      )}
    </Card>
  );
}

export function Apps({ apps, mac, win }: { apps: AppUsage[]; mac: string; win: Win }) {
  const [open, setOpen] = useState<string>("");
  const { rows, sort, pick, holdProps } = useSteadyRows(
    "client-apps",
    apps,
    (a) => a.app.id,
    {
      name: (a) => appName(a.app.name),
      category: (a) => category(a.app.category),
      down: (a) => a.down,
      up: (a) => a.up,
      flows: (a) => a.flows,
      share: (a) => a.share,
      now: (a) => (a.recent ? a.recent.down + a.recent.up : 0),
    },
    { col: "share", dir: "desc" },
  );
  const list = useRef<HTMLDivElement>(null);
  useFlip(list);
  const head = (label: ComponentChildren, col: string, first: "asc" | "desc" = "desc") => <SortHead label={label} col={col} sort={sort} pick={pick} first={first} />;
  return (
    <Card label="Client apps" title="Apps" aside={<span class="muted small">click a row for that app's domains and countries</span>}>
      {apps.length === 0 ? (
        <Empty>No apps in this period.</Empty>
      ) : (
        <div class="scroll-x">
          <div class="apps-table" ref={list} {...holdProps}>
            <div class="apps-grid apps-head">
              {head("App", "name", "asc")}
              {head("Category", "category", "asc")}
              {head("↓ down", "down")}
              {head("↑ up", "up")}
              {head("Flows", "flows")}
              {head("Share", "share")}
              {head(`Now, ${windowLabel(win)}`, "now")}
            </div>
            {rows.map((a) => {
              const isOpen = open === a.app.id;
              const name = a.other ? "other apps" : appName(a.app.name);
              return (
                <div key={a.app.id} data-key={a.app.id} class="apps-item">
                  <button type="button" class="apps-grid apps-row" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? "" : a.app.id)}>
                    <span class="strong app-cell">
                      <span class="caret" aria-hidden="true">{isOpen ? "▾" : "▸"}</span>
                      <AppIcon id={a.other ? "other" : a.app.id} name={name} size={24} />
                      <span class="ellipsis">{name}</span>
                    </span>
                    <span class="soft">{name.includes(" · ") ? "by organisation" : category(a.app.category) || "-"}</span>
                    <span>{bytes(a.down)}</span>
                    <span>{bytes(a.up)}</span>
                    <span>{num(a.flows)}</span>
                    <span>{share(a.share)}</span>
                    <span class="small">↓ {rate(a.recent?.down ?? 0)}</span>
                  </button>
                  {isOpen && <AppDetail a={a} mac={mac} />}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </Card>
  );
}

function AppDetail({ a, mac }: { a: AppUsage; mac: string }) {
  const domains = a.domains ?? [];
  const places = a.places ?? [];
  return (
    <div class="app-detail">
      {!a.other && (
        <div class="app-detail-actions">
          <BlockApp appId={a.app.id} name={a.app.name} mac={mac} />
        </div>
      )}
      <div class="col-list">
        <span class="muted small">Domains (name source)</span>
        {domains.length === 0 && <span class="muted small">no names</span>}
        {domains.map((d) => (
          <div key={d.name} class="row-between small-13">
            <span class="ellipsis">
              <EntityLink e={{ kind: "domain", name: d.other ? "" : d.name }}>{d.other ? "other domains" : d.name || "no name"}</EntityLink>
              {d.src && !d.other && <span class="muted"> · {nameSource(d.src)}</span>}
            </span>
            <span class="soft nowrap">
              ↓ {bytes(d.down)} · ↑ {bytes(d.up)}
            </span>
          </div>
        ))}
      </div>
      <div class="col-list">
        <span class="muted small">Where this app goes</span>
        {places.length === 0 && <span class="muted small">no countries</span>}
        {places.map((p) => (
          <div key={p.cc + p.org} class="place-row small-13">
            <CC cc={p.cc} />
            <EntityLink e={{ kind: "country", cc: p.other ? "--" : p.cc }} class="ellipsis">{p.other ? "others" : p.org || countryName(p.cc)}</EntityLink>
            <span class="soft nowrap">
              ↓ {bytes(p.down)} · ↑ {bytes(p.up)}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

export function Where({ dest }: { dest: Destinations }) {
  return (
    <Card label="Destinations" title="Destinations">
      <div class="where">
        <div class="col-list">
          <span class="muted small">Countries</span>
          {dest.countries.length === 0 && <span class="muted small">none</span>}
          {dest.countries.map((c) => (
            <div key={c.cc} class="row-between small-13">
              <span>
                <b>{c.cc}</b> <EntityLink e={{ kind: "country", cc: c.other ? "--" : c.cc }}>{c.other || c.cc === "--" ? "unknown or others" : countryName(c.cc)}</EntityLink>
              </span>
              <span class="soft nowrap" title={`↑ ${bytes(c.up)}`}>↓ {bytes(c.down)}</span>
            </div>
          ))}
        </div>
        <div class="col-list">
          <span class="muted small">Organisations (ASN)</span>
          {dest.orgs.length === 0 && <span class="muted small">none</span>}
          {dest.orgs.map((o) => (
            <div key={o.asn} class="row-between small-13">
              <span class="ellipsis">
                <EntityLink e={{ kind: "network", asn: o.other || o.asn === "other" ? 0 : o.asn }}>{o.other || o.asn === "other" ? "other organisations" : o.org || "no name"}</EntityLink>
                {!(o.other || o.asn === "other") && <span class="muted"> · AS{o.asn}</span>}
              </span>
              <span class="soft nowrap">↓ {bytes(o.down)}</span>
            </div>
          ))}
        </div>
        <div class="col-list">
          <span class="muted small">Domains</span>
          {dest.domains.length === 0 && <span class="muted small">none</span>}
          {dest.domains.map((dm) => (
            <div key={dm.name} class="row-between small-13">
              <span class="ellipsis">
                <EntityLink e={{ kind: "domain", name: dm.other ? "" : dm.name }}>{dm.other ? "other domains" : dm.name || "no name"}</EntityLink>
                {dm.app && <span class="muted"> · <EntityLink e={{ kind: "app", id: dm.app.id }}>{appName(dm.app.name)}</EntityLink></span>}
              </span>
              <span class="soft nowrap">↓ {bytes(dm.down)}</span>
            </div>
          ))}
        </div>
      </div>
      <div class="foot soft small">
        {plural(dest.countries.filter(isRealCountry).length, "country", "countries")} in the period
      </div>
    </Card>
  );
}
