// The floating cards on screen: several at once, above every screen (a card
// stays while the page underneath changes), dragged by the header, brought
// forward by a click, closed by X or by Esc (the one in front).

import { h, createContext, type ComponentChildren } from "preact";
import { useCallback, useContext, useEffect, useRef, useState } from "preact/hooks";
import { EMPTY, closeWin, focusWin, moveWin, openWin, topWin, type WinState } from "../windows";

const SIZE = { w: 380, h: 440 };

interface Card { title: string; body: () => ComponentChildren }
interface Api { open: (key: string, card: Card, anchor?: Element | null) => void }

const Ctx = createContext<Api>({ open: () => {} });
export const useCards = () => useContext(Ctx);

const view = () => ({ w: window.innerWidth, h: window.innerHeight });

export function CardsProvider({ children }: { children: ComponentChildren }) {
  const [st, setSt] = useState<WinState>(EMPTY);
  const cards = useRef(new Map<string, Card>());
  const open = useCallback((key: string, card: Card, anchor?: Element | null) => {
    cards.current.set(key, card);
    const r = anchor?.getBoundingClientRect();
    setSt((s) => openWin(s, key, r ? { x: r.left, y: r.top, w: r.width, h: r.height } : null, SIZE, view()));
  }, []);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      setSt((s) => {
        const t = topWin(s);
        return t ? closeWin(s, t.id) : s;
      });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return (
    <Ctx.Provider value={{ open }}>
      {children}
      <div class="wins">
        {st.wins.map((w) => {
          const card = cards.current.get(w.key);
          if (!card) return null;
          return (
            <Window
              key={w.id}
              x={w.x}
              y={w.y}
              z={w.z}
              title={card.title}
              onFocus={() => setSt((s) => focusWin(s, w.id))}
              onClose={() => setSt((s) => closeWin(s, w.id))}
              onMove={(x, y) => setSt((s) => moveWin(s, w.id, x, y, SIZE, view()))}
            >
              {card.body()}
            </Window>
          );
        })}
      </div>
    </Ctx.Provider>
  );
}

function Window(p: { x: number; y: number; z: number; title: string; onFocus: () => void; onClose: () => void; onMove: (x: number, y: number) => void; children: ComponentChildren }) {
  const drag = useRef<{ sx: number; sy: number; ox: number; oy: number } | null>(null);
  const down = (e: PointerEvent) => {
    if ((e.target as HTMLElement).closest("button")) return;
    drag.current = { sx: e.clientX, sy: e.clientY, ox: p.x, oy: p.y };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  };
  const move = (e: PointerEvent) => {
    const d = drag.current;
    if (d) p.onMove(d.ox + e.clientX - d.sx, d.oy + e.clientY - d.sy);
  };
  const up = () => {
    drag.current = null;
  };
  return (
    <section class="win" role="dialog" aria-label={p.title} style={{ left: `${p.x}px`, top: `${p.y}px`, zIndex: p.z }} onPointerDown={p.onFocus}>
      <div class="win-head" onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={up}>
        <span class="win-title">{p.title}</span>
        <button type="button" class="win-x" aria-label={`Close ${p.title}`} onClick={p.onClose}>✕</button>
      </div>
      <div class="win-body">{p.children}</div>
    </section>
  );
}
