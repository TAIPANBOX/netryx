// One change at a time per button: a second press while the first is still
// with the router is dropped, even one that arrives before the page has
// redrawn (a ref, not only state, holds it).

export interface Flag { current: boolean }

/** Runs fn unless one is already running on this flag; returns whether it ran. */
export async function once(flag: Flag, fn: () => Promise<void>): Promise<boolean> {
  if (flag.current) return false;
  flag.current = true;
  try {
    await fn();
  } finally {
    flag.current = false;
  }
  return true;
}
