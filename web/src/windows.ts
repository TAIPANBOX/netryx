// Floating cards: several open at once, each beside what was clicked, dragged
// by its header, brought forward by a click, closed by its X or by Esc (the
// top one). This is the state alone; screens/windows.tsx draws it. No DOM
// here, so node tests cover it.

export interface Win { id: number; key: string; x: number; y: number; z: number }
export interface WinState { wins: Win[]; next: number; top: number }

/** At most this many cards are open; a new one closes the oldest. */
export const MAX_WINDOWS = 6;
const GAP = 10;
const MARGIN = 12;

export const EMPTY: WinState = { wins: [], next: 1, top: 0 };

type Box = { x: number; y: number; w: number; h: number };
type Size = { w: number; h: number };

function clamp(x: number, y: number, size: Size, view: Size) {
  return {
    x: Math.min(Math.max(x, MARGIN), Math.max(MARGIN, view.w - size.w - MARGIN)),
    y: Math.min(Math.max(y, MARGIN), Math.max(MARGIN, view.h - size.h - MARGIN)),
  };
}

/** Where a card opens: right of what was clicked, else left of it, inside the window; centred without an anchor. */
export function placeNear(anchor: Box | null, size: Size, view: Size) {
  if (!anchor) return clamp((view.w - size.w) / 2, (view.h - size.h) / 2, size, view);
  const right = anchor.x + anchor.w + GAP;
  const x = right + size.w + MARGIN <= view.w ? right : anchor.x - GAP - size.w;
  return clamp(x, anchor.y, size, view);
}

/** Opens a card, or brings forward the one already open for the same thing. */
export function openWin(s: WinState, key: string, anchor: Box | null, size: Size, view: Size): WinState {
  const top = s.top + 1;
  const open = s.wins.find((w) => w.key === key);
  if (open) return { ...s, top, wins: s.wins.map((w) => (w.id === open.id ? { ...w, z: top } : w)) };
  const p = placeNear(anchor, size, view);
  let wins = [...s.wins, { id: s.next, key, x: p.x, y: p.y, z: top }];
  if (wins.length > MAX_WINDOWS) {
    const oldest = [...wins].sort((a, b) => a.id - b.id)[0];
    wins = wins.filter((w) => w.id !== oldest.id);
  }
  return { wins, next: s.next + 1, top };
}

export function closeWin(s: WinState, id: number): WinState {
  return { ...s, wins: s.wins.filter((w) => w.id !== id) };
}

export function focusWin(s: WinState, id: number): WinState {
  if (!s.wins.some((w) => w.id === id)) return s;
  const top = s.top + 1;
  return { ...s, top, wins: s.wins.map((w) => (w.id === id ? { ...w, z: top } : w)) };
}

/** The card in front, which Esc closes. */
export function topWin(s: WinState): Win | undefined {
  return s.wins.reduce<Win | undefined>((a, w) => (!a || w.z > a.z ? w : a), undefined);
}

export function moveWin(s: WinState, id: number, x: number, y: number, size: Size, view: Size): WinState {
  const p = clamp(x, y, size, view);
  return { ...s, wins: s.wins.map((w) => (w.id === id ? { ...w, x: p.x, y: p.y } : w)) };
}

/** Esc closes the card in front, unless the owner is typing in a field or something else already handled the key. */
export function escCloses(e: { key: string; defaultPrevented: boolean; target: { tagName?: string; isContentEditable?: boolean } | null }): boolean {
  if (e.key !== "Escape" || e.defaultPrevented) return false;
  const t = e.target;
  return !(t && (t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName ?? "")));
}

/** A card's size: 380 wide, never wider than the window less its margins; 440 high, as the page draws it. */
export function cardSize(view: Size): Size {
  return { w: Math.min(380, view.w - 2 * MARGIN), h: 440 };
}

/** Every card back inside the window after it changed size. */
export function refit(s: WinState, size: Size, view: Size): WinState {
  return { ...s, wins: s.wins.map((w) => ({ ...w, ...clamp(w.x, w.y, size, view) })) };
}
