// Calm live tables: rows hold their places and move at most every 30 s, and
// only when the new order is clearly different; any column sorts, either way,
// and the choice is remembered. No DOM here, so node tests cover it.

export interface Steady {
  /** Row keys in the order shown. */
  order: string[];
  /** When the order last moved, ms since the epoch. */
  at: number;
  /** The sort it was made for: another sort reorders at once. */
  sortKey: string;
}

export interface SortState { col: string; dir: "asc" | "desc" }

/** Rows move no more often than this... */
export const MIN_GAP_MS = 30_000;
/** ...and only when two neighbours are this far apart, as a share of the larger. */
export const MARGIN = 0.12;

/**
 * The order to show: `rows` come in the order the chosen sort wants, each with
 * the value that sort is about (NaN for a column that is not a number). A new
 * row takes its place at once and a gone one leaves; the rest move only once
 * MIN_GAP_MS has passed since the last move and two rows shown in the wrong
 * order differ by more than MARGIN.
 */
export function steadyOrder(prev: Steady | null, rows: { key: string; value: number }[], now: number, sortKey: string, hold = false): Steady {
  const desired = rows.map((r) => r.key);
  if (!prev || prev.sortKey !== sortKey) return { order: desired, at: now, sortKey };
  if (hold) {
    // The pointer is over the table: nothing moves under it; a new row waits
    // at the end and a gone one leaves.
    const present = new Set(desired);
    const kept = prev.order.filter((k) => present.has(k));
    const shown = new Set(kept);
    return { order: [...kept, ...desired.filter((k) => !shown.has(k))], at: prev.at, sortKey };
  }
  const rank = new Map(desired.map((k, i) => [k, i]));
  const value = new Map(rows.map((r) => [r.key, r.value]));
  // The rows still there, in the order shown, with each new row put before
  // the first shown row the sort wants after it.
  const kept = prev.order.filter((k) => rank.has(k));
  const shown = new Set(kept);
  for (const k of desired) {
    if (shown.has(k)) continue;
    const at = kept.findIndex((x) => (rank.get(x) ?? 0) > (rank.get(k) ?? 0));
    if (at < 0) kept.push(k);
    else kept.splice(at, 0, k);
    shown.add(k);
  }
  if (now - prev.at < MIN_GAP_MS) return { order: kept, at: prev.at, sortKey };
  // Any two rows shown in the wrong order, not only neighbours: values that
  // drift a little at a time can put a row far above the one shown first.
  for (let i = 0; i < kept.length; i++) {
    for (let j = i + 1; j < kept.length; j++) {
      const a = kept[i];
      const b = kept[j];
      if ((rank.get(b) ?? 0) >= (rank.get(a) ?? 0)) continue;
      const va = value.get(a) ?? NaN;
      const vb = value.get(b) ?? NaN;
      const big = Math.max(Math.abs(va), Math.abs(vb));
      if (Number.isNaN(va) || Number.isNaN(vb) || (big > 0 && Math.abs(va - vb) > MARGIN * big)) {
        return { order: desired, at: now, sortKey };
      }
    }
  }
  return { order: kept, at: prev.at, sortKey };
}

/** Rows sorted by one column, numbers by value and text in any case; ties keep the key order. */
export function sortRows<T>(rows: T[], key: (r: T) => string, get: Record<string, (r: T) => string | number>, s: SortState): T[] {
  const g = get[s.col];
  const byKey = rows.slice().sort((a, b) => (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0));
  if (!g) return byKey;
  const sign = s.dir === "asc" ? 1 : -1;
  return byKey.sort((a, b) => {
    const x = g(a);
    const y = g(b);
    const c = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y), "en", { sensitivity: "base" });
    return c * sign;
  });
}

/** A click on a column: the sorted one turns round, another starts its own way. */
export function nextSort(s: SortState, col: string, firstDir: "asc" | "desc"): SortState {
  if (s.col === col) return { col, dir: s.dir === "asc" ? "desc" : "asc" };
  return { col, dir: firstDir };
}

/** The remembered sort of a table, if it names one of its columns; storage may be missing. */
export function loadSort(table: string, fallback: SortState, cols: string[]): SortState {
  try {
    const raw = localStorage.getItem(`netryx.sort.${table}`);
    if (!raw) return fallback;
    const v = JSON.parse(raw) as Partial<SortState>;
    if (typeof v.col === "string" && cols.includes(v.col) && (v.dir === "asc" || v.dir === "desc")) return { col: v.col, dir: v.dir };
  } catch {
    // no storage, or not ours: the table's own default
  }
  return fallback;
}

export function saveSort(table: string, s: SortState) {
  try {
    localStorage.setItem(`netryx.sort.${table}`, JSON.stringify(s));
  } catch {
    // not remembered, still applied
  }
}
