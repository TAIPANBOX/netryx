// The router's time zone, which every time on the page is shown in. The page
// learns it once with the session (GET /api/session "zone"); until then, and
// when the router names none or a name this browser does not know, it is UTC.
// No zone is written into the page.

let current = "UTC";

export function setZone(name: string | undefined): void {
  if (!name) {
    current = "UTC";
    return;
  }
  try {
    new Intl.DateTimeFormat("en-GB", { timeZone: name });
    current = name;
  } catch {
    current = "UTC";
  }
}

export const zone = (): string => current;

const fmts = new Map<string, Intl.DateTimeFormat>();
function wallFormat(z: string): Intl.DateTimeFormat {
  let f = fmts.get(z);
  if (!f) {
    f = new Intl.DateTimeFormat("en-GB", {
      timeZone: z, year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
    });
    fmts.set(z, f);
  }
  return f;
}

/** The wall clock of a moment in a zone. */
export function wall(ms: number, z: string = current): { y: number; mo: number; d: number; h: number; mi: number; s: number } {
  const o: Record<string, number> = {};
  for (const p of wallFormat(z).formatToParts(new Date(ms))) if (p.type !== "literal") o[p.type] = parseInt(p.value, 10);
  return { y: o.year, mo: o.month, d: o.day, h: o.hour, mi: o.minute, s: o.second };
}

/** The moment (ms) at which the zone's wall clock reads y-mo-d h:mi; a day is added past the end of the month by Date.UTC. */
export function atWall(y: number, mo: number, d: number, h: number, mi: number, z: string = current): number {
  const naive = Date.UTC(y, mo - 1, d, h, mi);
  let guess = naive;
  for (let i = 0; i < 2; i++) {
    const w = wall(guess, z);
    guess += naive - Date.UTC(w.y, w.mo - 1, w.d, w.h, w.mi, w.s);
  }
  return guess;
}
