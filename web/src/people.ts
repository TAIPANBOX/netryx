// The house's people: an id for a new one, and whose a device is.

import type { Person } from "./types";

/** An id for a new person: p_ and the name in Latin letters, or the first free p_<n>. */
export function personId(name: string, taken: string[]): string {
  const slug = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 24)
    .replace(/_+$/, "");
  if (slug) {
    const base = `p_${slug}`;
    if (!taken.includes(base)) return base;
    for (let i = 2; ; i++) if (!taken.includes(`${base}_${i}`)) return `${base}_${i}`;
  }
  for (let i = 1; ; i++) if (!taken.includes(`p_${i}`)) return `p_${i}`;
}

/** The person a device belongs to. */
export function ownerOf(people: Person[] | undefined, mac: string): { id: string; name: string } | undefined {
  const p = (people ?? []).find((x) => x.macs.includes(mac));
  return p ? { id: p.id, name: p.name } : undefined;
}
