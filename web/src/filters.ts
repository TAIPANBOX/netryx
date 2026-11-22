// The house's DNS filters as the console shows and sends them
// (GET and PUT /api/filters), and adblock-fast's sources (/api/adblock).
// No DOM here, so node tests cover it.

export type YouTubeLevel = "off" | "moderate" | "strict";

export interface Filters {
  forceDns: boolean;
  blockDot: boolean;
  blockDoh: boolean;
  safeSearch: boolean;
  youtube: YouTubeLevel;
  safeDns?: { healthy: boolean; problem: string };
}

export type FiltersBody = Omit<Filters, "safeDns">;

const LEVELS: YouTubeLevel[] = ["off", "moderate", "strict"];

/** The PUT body: the five settings, an unknown YouTube level sent as off. */
export function filtersBody(f: Filters): FiltersBody {
  return {
    forceDns: !!f.forceDns,
    blockDot: !!f.blockDot,
    blockDoh: !!f.blockDoh,
    safeSearch: !!f.safeSearch,
    youtube: LEVELS.includes(f.youtube) ? f.youtube : "off",
  };
}

/** The safe resolver's state line. */
export function safeDnsText(f: Filters): string {
  if (!f.safeSearch && f.youtube === "off") return "DNS filter off";
  if (f.safeDns?.healthy) return "DNS filter working";
  return `DNS filter not working: ${f.safeDns?.problem || "no group has the filter"}`;
}

export interface AdblockSource {
  id: string;
  name: string;
  url: string;
  enabled: boolean;
}

export interface Adblock {
  installed: boolean;
  sources: AdblockSource[];
  restartNote: string;
}
