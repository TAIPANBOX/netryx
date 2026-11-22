// Pause: one switch for the whole page. While it is on no poll asks the
// router again, so the figures hold still while somebody reads them.

let paused = false;
const listeners = new Set<(p: boolean) => void>();

export const isPaused = () => paused;

export function setPaused(v: boolean) {
  if (v === paused) return;
  paused = v;
  for (const fn of listeners) fn(v);
}

export function onPause(fn: (p: boolean) => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
