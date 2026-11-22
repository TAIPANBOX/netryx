// Calm live tables: sortable headers, an order that holds still (steady.ts),
// and rows that glide to their new place when the order does move.

import { h, type ComponentChildren, type RefObject } from "preact";
import { useLayoutEffect, useRef, useState } from "preact/hooks";
import { loadSort, nextSort, saveSort, sortRows, steadyOrder, type SortState, type Steady } from "../steady";

/**
 * Rows sorted by the column the viewer picked (remembered per table), in an
 * order that moves at most every 30 s and only for a clear difference, and
 * not at all while the pointer is over the table (spread holdProps on it).
 */
export function useSteadyRows<T>(table: string, rows: T[], key: (r: T) => string, get: Record<string, (r: T) => string | number>, fallback: SortState) {
  const [sort, setSort] = useState<SortState>(() => loadSort(table, fallback, Object.keys(get)));
  const steady = useRef<Steady | null>(null);
  // While the pointer is over the table its order holds, so a click lands on
  // the row that was under it.
  const [hold, setHold] = useState(false);
  const sorted = sortRows(rows, key, get, sort);
  const g = get[sort.col];
  steady.current = steadyOrder(
    steady.current,
    sorted.map((r) => {
      const v = g ? g(r) : NaN;
      return { key: key(r), value: typeof v === "number" ? v : NaN };
    }),
    Date.now(),
    `${sort.col}:${sort.dir}`,
    hold,
  );
  const byKey = new Map(rows.map((r) => [key(r), r]));
  const ordered = steady.current.order.map((k) => byKey.get(k)).filter((r): r is T => r !== undefined);
  const pick = (col: string, firstDir: "asc" | "desc") => {
    const n = nextSort(sort, col, firstDir);
    setSort(n);
    saveSort(table, n);
  };
  const holdProps = { onPointerEnter: () => setHold(true), onPointerLeave: () => setHold(false) };
  return { rows: ordered, sort, pick, holdProps };
}

function arrow(sort: SortState, cols: string[]) {
  return cols.includes(sort.col) ? (sort.dir === "asc" ? " ▴" : " ▾") : "";
}

/** A sortable header cell of a table. */
export function SortTh(p: { label: ComponentChildren; col: string; sort: SortState; pick: (c: string, d: "asc" | "desc") => void; first?: "asc" | "desc"; num?: boolean; also?: string[] }) {
  const on = p.sort.col === p.col || (p.also ?? []).includes(p.sort.col);
  return (
    <th class={p.num ? "num" : undefined} aria-sort={on ? (p.sort.dir === "asc" ? "ascending" : "descending") : "none"}>
      <button type="button" class={on ? "th-sort active" : "th-sort"} onClick={() => p.pick(p.col, p.first ?? "desc")}>
        {p.label}
        <span aria-hidden="true">{arrow(p.sort, [p.col, ...(p.also ?? [])])}</span>
      </button>
    </th>
  );
}

/** The same for a grid header (the client's apps, the flows). */
export function SortHead(p: { label: ComponentChildren; col: string; sort: SortState; pick: (c: string, d: "asc" | "desc") => void; first?: "asc" | "desc"; also?: string[] }) {
  const on = p.sort.col === p.col || (p.also ?? []).includes(p.sort.col);
  return (
    <button type="button" class={on ? "th-sort active" : "th-sort"} aria-pressed={on} onClick={() => p.pick(p.col, p.first ?? "desc")}>
      {p.label}
      <span aria-hidden="true">{arrow(p.sort, [p.col, ...(p.also ?? [])])}</span>
    </button>
  );
}

/**
 * Rows that change place glide there (FLIP): each child with a data-key is
 * measured after every render, and one that moved is shown where it was and
 * eased to where it is. Positions are relative to the container, so scrolling
 * the page moves nothing; a viewer who asked for less motion gets none.
 */
export function useFlip(ref: RefObject<HTMLElement>) {
  const pos = useRef(new Map<string, number>());
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const still = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
    const next = new Map<string, number>();
    for (const c of Array.from(el.children) as HTMLElement[]) {
      const k = c.dataset.key;
      if (!k) continue;
      const top = c.offsetTop;
      next.set(k, top);
      const before = pos.current.get(k);
      if (still || before === undefined || Math.abs(before - top) < 1) continue;
      c.style.transition = "none";
      c.style.transform = `translateY(${before - top}px)`;
      // Read layout so the start position takes effect before the move.
      void c.offsetHeight;
      requestAnimationFrame(() => {
        c.style.transition = "transform 450ms ease";
        c.style.transform = "";
      });
    }
    pos.current = next;
  });
}
