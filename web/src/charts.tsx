// Hand-written SVG charts, after the approved mockup: download stacked above
// the axis, upload hanging below it, and a three-column Sankey. No chart
// library: the page has a 60 KB budget and must fetch nothing from outside.
//
// Colours and sizes that change per value go through SVG presentation
// attributes or Preact's style object (set via CSSOM), never a style string:
// the page's CSP refuses inline style.

import { h, Fragment, type ComponentChildren } from "preact";
import { useContext, useEffect, useState } from "preact/hooks";
import { hourSpark, type HourSpark } from "./live";
import { areaPath, columnOfTime, crosshairRows, cumulate, legendAfter, peakColumn, pickColumn, sankeyLayout, type Marker, type SankeyLink, type SankeyNode } from "./geometry";
import { clock, date, dayMonth, rate, time } from "./format";
import { brushSpan, setSpan } from "./span";
import { appIcon } from "./icons";
import { IconContext, brokenIcons, iconBroke } from "./ui";

/** id tells layers apart when two share a label (two clients of one name). */
export interface Layer { label: string; color: string; values: number[]; id?: string }

interface StackedProps {
  /** Download layers, bottom first, in bytes per second. */
  layers: Layer[];
  /** Upload of everything, in bytes per second. */
  up: number[];
  viewH: number;
  axis: number;
  downH: number;
  upH: number;
  label: string;
  /** Called with the column clicked, when the chart leads somewhere. */
  onPick?: (column: number) => void;
  /** Each column's start (RFC 3339) and the seconds it covers: what the crosshair, the shared cursor and the brush read. */
  t?: string[];
  stepSec?: number;
  /** A drag across columns picks that stretch for the whole screen. */
  brush?: boolean;
  /** Events drawn on the time axis. */
  markers?: Marker[];
  /** Layers the legend hid, and the one it points at. */
  hidden?: string[];
  focus?: string;
}

// The time under the mouse on any chart: every chart of the screen draws a
// line at it, so a spike on one is seen against the others.
let cursorMs: number | null = null;
const cursorSubs = new Set<() => void>();
function setCursor(ms: number | null) {
  if (ms === cursorMs) return;
  cursorMs = ms;
  cursorSubs.forEach((f) => f());
}
function useCursor(): number | null {
  const [, force] = useState(0);
  useEffect(() => {
    const f = () => force((n) => n + 1);
    cursorSubs.add(f);
    return () => void cursorSubs.delete(f);
  }, []);
  return cursorMs;
}

const layerId = (l: Layer) => l.id ?? l.label;
const colX = (i: number, n: number) => (n <= 1 ? 500 : (i * 1000) / (n - 1));

/**
 * Download layers stacked above the axis and the upload area below it. The two
 * sides have their own scales, because upload is usually a tenth of download
 * and would vanish on a shared one; each scale's top is written on the chart.
 * The mouse shows what each layer moved at that moment; a drag picks a stretch.
 */
export function StackedArea(p: StackedProps) {
  const hidden = p.hidden ?? [];
  const shown = p.layers.filter((l) => !hidden.includes(layerId(l)));
  const cum = cumulate(shown.map((l) => l.values));
  const top = cum[cum.length - 1] ?? [];
  const maxDown = Math.max(0, ...top);
  const maxUp = Math.max(0, ...p.up);
  const n = p.up.length;
  const t = p.t ?? [];
  const step = p.stepSec ?? 0;
  const [hover, setHover] = useState(-1);
  const [drag, setDrag] = useState<[number, number] | null>(null);
  const shared = useCursor();
  const paths = [];
  // Drawn from the full stack down, so each band shows the colour of its layer.
  for (let j = cum.length - 1; j >= 0; j--) {
    const id = layerId(shown[j]);
    paths.push(<path key={id} class={p.focus && p.focus !== id ? "layer layer-dim" : "layer"} d={areaPath(cum[j], maxDown, p.axis, p.downH, -1)} fill={shown[j].color} />);
  }
  const colAt = (e: MouseEvent) => {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
    return pickColumn((e.clientX - r.left) / r.width, n);
  };
  const live = n > 0 && t.length === n && step > 0;
  // Another chart's cursor, when this one is not under the mouse.
  const other = hover < 0 && shared !== null && live ? columnOfTime(t, step, shared) : -1;
  const cross = hover >= 0 && live ? crosshairRows(hover, shown, p.up, []) : null;
  const x = (i: number) => colX(i, n);
  return (
    <div
      class={p.onPick || p.brush ? "chart chart-pick" : "chart"}
      onMouseMove={(e) => {
        if (!live) return;
        const c = colAt(e);
        setHover(c);
        setCursor(Date.parse(t[c]));
        if (drag) setDrag([drag[0], c]);
      }}
      onMouseLeave={() => {
        setHover(-1);
        setDrag(null);
        setCursor(null);
      }}
      onMouseDown={(e) => {
        if (e.button === 0 && n > 0) setDrag([colAt(e), colAt(e)]);
      }}
      onMouseUp={(e) => {
        const d = drag;
        setDrag(null);
        if (!d || n === 0) return;
        if (p.brush && live && d[0] !== d[1]) {
          const sp = brushSpan(t, step, d[0], d[1]);
          if (sp) setSpan(sp);
        } else if (p.onPick) p.onPick(colAt(e));
      }}
    >
      <svg viewBox={`0 0 1000 ${p.viewH}`} preserveAspectRatio="none" role="img" aria-label={p.label} class="chart-svg" height={p.viewH}>
        <line class="grid" x1="0" y1={p.axis - p.downH} x2="1000" y2={p.axis - p.downH} />
        <line class="grid" x1="0" y1={p.axis - p.downH / 2} x2="1000" y2={p.axis - p.downH / 2} />
        {paths}
        <path class="up-area" d={areaPath(p.up, maxUp, p.axis, p.upH, 1)} />
        <line class="axis" x1="0" y1={p.axis} x2="1000" y2={p.axis} />
        {drag && drag[0] !== drag[1] && (
          <rect class="brush" x={x(Math.min(...drag))} y="0" width={Math.max(1, x(Math.max(...drag)) - x(Math.min(...drag)))} height={p.viewH} />
        )}
        {(hover >= 0 || other >= 0) && live && (
          <line class={hover >= 0 ? "cursor" : "cursor cursor-shared"} x1={x(hover >= 0 ? hover : other)} y1="0" x2={x(hover >= 0 ? hover : other)} y2={p.viewH} vector-effect="non-scaling-stroke" />
        )}
      </svg>
      <span class="chart-scale chart-scale-down">↓ {rate(maxDown)}</span>
      <span class="chart-scale chart-scale-up">↑ {rate(maxUp)}</span>
      {(p.markers ?? []).map((m) => (
        <span key={m.at + m.text} class={`marker marker-${m.kind}`} style={{ left: `${m.x / 10}%` }} title={m.text} aria-label={m.text} role="img" />
      ))}
      {cross && (
        <div class={hover > n / 2 ? "chart-tip chart-tip-left" : "chart-tip"} style={{ left: `${x(hover) / 10}%` }} role="status">
          <strong>{stepSpan(t[hover], step)}</strong>
          {cross.rows.slice(0, 6).map((r) => (
            <span key={r.label} class="tip-row">
              <Swatch color={r.color} />
              <span class="ellipsis">{r.label}</span>
              <span class="right">{rate(r.rate)}</span>
            </span>
          ))}
          {cross.rows.length > 6 && <span class="muted">and {cross.rows.length - 6} more</span>}
          <span class="tip-row soft">
            <span>↓ {rate(cross.down)}</span>
            <span class="right">↑ {rate(cross.up)}</span>
          </span>
          {p.brush && <span class="muted">drag to look at a stretch</span>}
        </div>
      )}
    </div>
  );
}

/** "14:20 to 14:30", or a day's date for a day step. */
function stepSpan(iso: string, stepSec: number): string {
  if (stepSec >= 86400) return date(iso);
  if (stepSec < 60) return clock(iso);
  const end = new Date(Date.parse(iso) + stepSec * 1000).toISOString();
  return `${stepSec >= 3600 ? dayMonth(iso) + " " : ""}${time(iso)} to ${time(end)}`;
}

/** One download area and one upload area: the "total" view. */
export function AreaChart(p: Omit<StackedProps, "layers"> & { down: number[] }) {
  return <StackedArea {...p} layers={[{ label: "down", color: "#4C9AFF", values: p.down }]} />;
}

/**
 * A chart and its legend together: a legend entry's swatch hides or shows its
 * layer, a double click shows it alone (or all again), and pointing at an
 * entry fades the others. "What made this spike?" picks the busiest step.
 */
export function TimeChart(p: StackedProps & { legend: LegendItem[]; ticks?: string[]; spike?: boolean }) {
  const [hidden, setHidden] = useState<string[]>([]);
  const [focus, setFocus] = useState("");
  const ids = p.layers.map(layerId);
  const t = p.t ?? [];
  const step = p.stepSec ?? 0;
  const shown = p.layers.filter((l) => !hidden.includes(layerId(l)));
  const total = p.up.map((_, i) => shown.reduce((a, l) => a + (l.values[i] ?? 0), 0));
  const peak = p.spike && p.brush ? peakColumn(total) : -1;
  return (
    <Fragment>
      <StackedArea {...p} hidden={hidden} focus={focus} />
      <Legend
        items={p.legend}
        ticks={p.ticks}
        hidden={hidden}
        layers={ids}
        onToggle={(id, how) => setHidden(legendAfter(hidden, id, how, ids))}
        onFocus={setFocus}
        extra={
          peak >= 0 && t.length > 1 && step > 0 ? (
            <button type="button" class="link-btn small" onClick={() => { const sp = brushSpan(t, step, peak, peak); if (sp) setSpan(sp); }}>
              What made this spike? ({stepSpan(t[peak], step)})
            </button>
          ) : undefined
        }
      />
    </Fragment>
  );
}

/** A small colour square for a legend. */
export function Swatch({ color }: { color: string }) {
  return <span class="swatch" style={{ background: color }} />;
}

/** A list row's last hour: the counted steps only, its peak in the tooltip. */
export function HourSparkline({ s, color = "var(--accent, #4C9AFF)" }: { s?: HourSpark; color?: string }) {
  const p = hourSpark(s, 96, 24);
  const svg = (
    <svg viewBox="0 0 96 24" preserveAspectRatio="none" role="img" aria-label={p.label || "no hour yet"} class="spark spark-hour">
      <title>{p.label}</title>
      {p.d && <path d={p.d} fill="none" stroke={color} stroke-width="1.5" vector-effect="non-scaling-stroke" />}
    </svg>
  );
  // A line shortened by a restart says so under it, not only in the tooltip.
  if (!s || s.known <= 0) return svg;
  return (
    <span class="spark-wrap">
      {svg}
      <span class="muted spark-note">{s.known >= s.bytes.length ? "not counted yet" : p.label.split(",")[0]}</span>
    </span>
  );
}

export interface LegendItem { label: string; color: string; node?: ComponentChildren; id?: string }

export function Legend({ items, ticks, hidden = [], layers = [], onToggle, onFocus, extra }: {
  items: LegendItem[]; ticks?: string[]; hidden?: string[]; layers?: string[];
  onToggle?: (id: string, how: "toggle" | "isolate") => void; onFocus?: (id: string) => void; extra?: ComponentChildren;
}) {
  return (
    <div class="legend">
      {items.map((g) => {
        const id = g.id ?? g.label;
        const layer = !!onToggle && layers.includes(id);
        const off = hidden.includes(id);
        return (
          <span class={off ? "legend-item legend-off" : "legend-item"} key={id} onMouseEnter={() => layer && onFocus?.(id)} onMouseLeave={() => layer && onFocus?.("")}>
            {layer ? (
              <button type="button" class="swatch-btn" aria-pressed={!off} title="Click to hide or show, double click to show it alone"
                onClick={() => onToggle!(id, "toggle")} onDblClick={() => onToggle!(id, "isolate")}>
                <Swatch color={g.color} />
              </button>
            ) : (
              <Swatch color={g.color} />
            )}
            {g.node ?? g.label}
          </span>
        );
      })}
      {extra}
      {ticks && ticks.length > 0 && <span class="legend-ticks">{ticks.join(" · ")}</span>}
    </div>
  );
}

/**
 * A track with coloured segments, each a percent of the track. Used for a
 * client's download and upload share and for a country's share.
 */
export function Bars({ parts, thin }: { parts: { pct: number; kind: "down" | "up" }[]; thin?: boolean }) {
  return (
    <div class={thin ? "bars bars-thin" : "bars"}>
      {parts.map((s, i) => (
        <span key={i} class={`bar bar-${s.kind}`} style={{ width: `${Math.max(0, Math.min(100, s.pct)).toFixed(2)}%` }} />
      ))}
    </div>
  );
}

function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

/**
 * Bands whose width is bytes, in three columns (client, apps, countries) or
 * more (the connection map). A node with an href is a link.
 */
export function Sankey({ nodes, links, label, cols = 3, width = 600, height = 300, href = {} }: {
  nodes: SankeyNode[]; links: SankeyLink[]; label: string; cols?: number; width?: number; height?: number; href?: Record<string, string>;
}) {
  const out = sankeyLayout(nodes, links, { height, top: 30, bottom: 20, gap: 14 }, cols, width);
  if (out.nodes.length === 0) return <p class="empty">No traffic in this period.</p>;
  const last = cols - 1;
  return (
    <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label} class="sankey">
      {out.links.map((l, i) => (
        <path key={i} class="sankey-link" d={l.d} stroke={l.color} stroke-width={Math.max(1, l.w).toFixed(1)} />
      ))}
      {out.nodes.map((n) => {
        const icon = n.col === 1 && n.icon;
        // Three columns label the last on its left; more columns label every
        // one on its right, where the layout left them room.
        const [tx, ty, anchor] =
          n.col === 0 ? [n.x, n.y - 8, "start"] : n.col === last && cols <= 3 ? [n.x - 8, n.y + n.h / 2 + 4, "end"] : [n.x + (icon ? 36 : 16), n.y + n.h / 2 + 4, "start"];
        const room = cols > 3 ? 22 : icon ? 23 : 26;
        const body = (
          <Fragment>
            <rect x={n.x} y={n.y} width="10" height={Math.max(1, n.h).toFixed(1)} rx="2" fill={n.color} />
            {icon && <SankeyIcon x={n.x + 16} y={n.y + n.h / 2 - 8} id={icon} name={n.name ?? ""} />}
            <text class={href[n.id] ? "sankey-label sankey-link-label" : "sankey-label"} x={tx} y={ty} text-anchor={anchor}>{clip(n.label, room)}</text>
          </Fragment>
        );
        return href[n.id] ? (
          <a key={n.id} href={href[n.id]} aria-label={n.label}>{body}</a>
        ) : (
          <Fragment key={n.id}>{body}</Fragment>
        );
      })}
    </svg>
  );
}

/** A 16 px app tile inside the Sankey, after AppIcon: image from /icons/, or the letter. */
function SankeyIcon({ x, y, id, name }: { x: number; y: number; id: string; name: string }) {
  const i = appIcon(id, useContext(IconContext), name);
  const [failed, setFailed] = useState("");
  const showImg = !!i.src && failed !== i.src && !brokenIcons.has(i.src);
  const glyph = i.kind === "svg" ? (i.dark ? "sankey-glyph dark" : "sankey-glyph") : "sankey-fav";
  return (
    <g aria-hidden="true">
      <rect x={x} y={y} width="16" height="16" rx="4" fill={i.color} />
      {showImg ? (
        <image href={i.src} x={x + 2.5} y={y + 2.5} width="11" height="11" class={glyph} onError={() => iconBroke(i.src ?? "", setFailed)} />
      ) : (
        <text x={x + 8} y={y + 12} text-anchor="middle" class={i.dark ? "sankey-letter dark" : "sankey-letter"}>{i.letter}</text>
      )}
    </g>
  );
}
