// Hand-written SVG charts, after the approved mockup: download stacked above
// the axis, upload hanging below it, and a three-column Sankey. No chart
// library: the page has a 60 KB budget and must fetch nothing from outside.
//
// Colours and sizes that change per value go through SVG presentation
// attributes or Preact's style object (set via CSSOM), never a style string:
// the page's CSP refuses inline style.

import { h, Fragment, type ComponentChildren } from "preact";
import { useContext, useState } from "preact/hooks";
import { areaPath, cumulate, sankeyLayout, type SankeyLink, type SankeyNode } from "./geometry";
import { rate } from "./format";
import { appIcon } from "./icons";
import { IconContext, brokenIcons, iconBroke } from "./ui";

export interface Layer { label: string; color: string; values: number[] }

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
}

/**
 * Download layers stacked above the axis and the upload area below it. The two
 * sides have their own scales, because upload is usually a tenth of download
 * and would vanish on a shared one; each scale's top is written on the chart.
 */
export function StackedArea(p: StackedProps) {
  const cum = cumulate(p.layers.map((l) => l.values));
  const top = cum[cum.length - 1] ?? [];
  const maxDown = Math.max(0, ...top);
  const maxUp = Math.max(0, ...p.up);
  const paths = [];
  // Drawn from the full stack down, so each band shows the colour of its layer.
  for (let j = cum.length - 1; j >= 0; j--) {
    paths.push(<path key={p.layers[j].label} class="layer" d={areaPath(cum[j], maxDown, p.axis, p.downH, -1)} fill={p.layers[j].color} />);
  }
  return (
    <div class="chart">
      <svg
        viewBox={`0 0 1000 ${p.viewH}`}
        preserveAspectRatio="none"
        role="img"
        aria-label={p.label}
        class={p.onPick ? "chart-svg chart-pick" : "chart-svg"}
        height={p.viewH}
        onClick={(e) => {
          const n = p.up.length;
          if (!p.onPick || n === 0) return;
          const r = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
          p.onPick(Math.min(n - 1, Math.max(0, Math.floor(((e.clientX - r.left) / r.width) * n))));
        }}
      >
        <line class="grid" x1="0" y1={p.axis - p.downH} x2="1000" y2={p.axis - p.downH} />
        <line class="grid" x1="0" y1={p.axis - p.downH / 2} x2="1000" y2={p.axis - p.downH / 2} />
        {paths}
        <path class="up-area" d={areaPath(p.up, maxUp, p.axis, p.upH, 1)} />
        <line class="axis" x1="0" y1={p.axis} x2="1000" y2={p.axis} />
      </svg>
      <span class="chart-scale chart-scale-down">↓ {rate(maxDown)}</span>
      <span class="chart-scale chart-scale-up">↑ {rate(maxUp)}</span>
    </div>
  );
}

/** One download area and one upload area: the "total" view. */
export function AreaChart(p: Omit<StackedProps, "layers"> & { down: number[] }) {
  return <StackedArea {...p} layers={[{ label: "down", color: "#4C9AFF", values: p.down }]} />;
}

/** A small colour square for a legend. */
export function Swatch({ color }: { color: string }) {
  return <span class="swatch" style={{ background: color }} />;
}

export function Legend({ items, ticks }: { items: { label: string; color: string; node?: ComponentChildren }[]; ticks?: string[] }) {
  return (
    <div class="legend">
      {items.map((g) => (
        <span class="legend-item" key={g.label}>
          <Swatch color={g.color} />
          {g.node ?? g.label}
        </span>
      ))}
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
