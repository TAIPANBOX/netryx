// The card a name shows when the mouse rests on it for 400 ms: a few facts
// about the thing, read over the last day, and a link to open it. A click on
// the card keeps it open until its X, Esc, a click elsewhere or a move to
// another screen. Touch has no hover, so it shows no card.

import { h } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Entity } from "../entity";
import { getJSON } from "../api";
import { hoverEntity, hoverPath, hoverSummary } from "../hover";

const DELAY_MS = 400;
const HIDE_MS = 250;
const CACHE_MS = 30_000;
const WIDTH = 300;

interface Shown { e: Entity; href: string; top: number; left: number; above: boolean }

const cache = new Map<string, { at: number; data: unknown }>();

export function HoverCards() {
  const [shown, setShown] = useState<Shown | null>(null);
  const [data, setData] = useState<{ path: string; data?: unknown; error?: string } | null>(null);
  const [pinned, setPinned] = useState(false);
  const show = useRef<number>(0);
  const hide = useRef<number>(0);
  const card = useRef<HTMLDivElement>(null);
  const pinnedRef = useRef(false);
  pinnedRef.current = pinned;

  const close = () => {
    clearTimeout(show.current);
    clearTimeout(hide.current);
    setShown(null);
    setPinned(false);
  };

  useEffect(() => {
    const linkOf = (t: EventTarget | null) => {
      const el = t instanceof Element ? t.closest("a[href^='#/']") : null;
      return el && !el.closest(".hover-card, .nav, .crumbs") ? (el as HTMLAnchorElement) : null;
    };
    const over = (ev: PointerEvent) => {
      if (ev.pointerType !== "mouse") return;
      if (card.current && ev.target instanceof Node && card.current.contains(ev.target)) {
        clearTimeout(hide.current);
        return;
      }
      const a = linkOf(ev.target);
      if (!a) return;
      const href = a.getAttribute("href") ?? "";
      const e = hoverEntity(href);
      if (!e) return;
      clearTimeout(show.current);
      clearTimeout(hide.current);
      show.current = window.setTimeout(() => {
        if (pinnedRef.current) return;
        const r = a.getBoundingClientRect();
        const above = r.bottom + 180 > window.innerHeight && r.top > 200;
        setShown({ e, href, top: above ? r.top - 6 : r.bottom + 6, left: Math.max(8, Math.min(r.left, window.innerWidth - WIDTH - 8)), above });
      }, DELAY_MS);
    };
    const out = (ev: PointerEvent) => {
      if (ev.pointerType !== "mouse") return;
      const to = ev.relatedTarget as Node | null;
      if (card.current && to && card.current.contains(to)) return;
      if (linkOf(ev.target) && to && linkOf(ev.target)?.contains(to)) return;
      clearTimeout(show.current);
      if (!pinnedRef.current) hide.current = window.setTimeout(() => setShown(null), HIDE_MS);
    };
    const key = (ev: KeyboardEvent) => ev.key === "Escape" && close();
    const down = (ev: PointerEvent) => {
      if (card.current && ev.target instanceof Node && !card.current.contains(ev.target)) close();
    };
    document.addEventListener("pointerover", over);
    document.addEventListener("pointerout", out);
    document.addEventListener("keydown", key);
    document.addEventListener("pointerdown", down);
    window.addEventListener("hashchange", close);
    const scroll = () => !pinnedRef.current && setShown(null);
    window.addEventListener("scroll", scroll, { passive: true });
    return () => {
      document.removeEventListener("pointerover", over);
      document.removeEventListener("pointerout", out);
      document.removeEventListener("keydown", key);
      document.removeEventListener("pointerdown", down);
      window.removeEventListener("hashchange", close);
      window.removeEventListener("scroll", scroll);
    };
  }, []);

  // A thing's facts are read once and kept for half a minute.
  useEffect(() => {
    if (!shown) return;
    const path = hoverPath(shown.e);
    const hit = cache.get(path);
    if (hit && Date.now() - hit.at < CACHE_MS) {
      setData({ path, data: hit.data });
      return;
    }
    setData({ path });
    const ac = new AbortController();
    getJSON<unknown>(path, ac.signal)
      .then((d) => {
        cache.set(path, { at: Date.now(), data: d });
        setData({ path, data: d });
      })
      .catch((err: Error) => !ac.signal.aborted && setData({ path, error: err.message }));
    return () => ac.abort();
  }, [shown?.href]);

  if (!shown) return null;
  const ready = data && data.path === hoverPath(shown.e) ? data : null;
  const s = ready?.data !== undefined ? hoverSummary(shown.e, ready.data) : null;
  return (
    <div
      ref={card}
      class={pinned ? "hover-card pinned" : "hover-card"}
      role="dialog"
      aria-label="About this name"
      style={{ top: `${shown.top}px`, left: `${shown.left}px`, width: `${WIDTH}px`, transform: shown.above ? "translateY(-100%)" : undefined }}
      onPointerLeave={(ev) => ev.pointerType === "mouse" && !pinned && (hide.current = window.setTimeout(() => setShown(null), HIDE_MS))}
      onClick={() => setPinned(true)}
    >
      <div class="row-between">
        <span class="strong ellipsis">{s?.title ?? "…"}</span>
        {pinned && (
          <button type="button" class="btn-mini" aria-label="Close" onClick={(ev) => { ev.stopPropagation(); close(); }}>
            ×
          </button>
        )}
      </div>
      {ready?.error && <p class="bad-text small">The router is not answering: {ready.error}.</p>}
      {!s && !ready?.error && <p class="muted small">Loading…</p>}
      {s && s.lines.map((l) => <p key={l} class="soft small hover-line">{l}</p>)}
      <a class="small" href={shown.href}>Open</a>
      {!pinned && <span class="muted small"> · click the card to keep it</span>}
    </div>
  );
}
