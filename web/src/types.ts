// The JSON the router's API returns, mirrored from internal/api/types.go.
// Bytes are counts, rates are bytes per second, times are RFC 3339 UTC.

export type Range = "1h" | "24h" | "7d" | "30d";

export interface App { id: string; name: string; category: string }

// stepSec is how many seconds the point covers: a series may mix hours
// (before a restart) and minutes (after it).
export interface SeriesPoint { t: string; stepSec?: number; down: Record<string, number>; up: Record<string, number> }

export interface Wan {
  up: boolean; device: string; ipv4: string; ipv6: string; uptimeSec: number;
  rxRate: number; txRate: number;
  latencyMs: number | null; jitterMs: number | null; lossPct: number;
}

// other marks a rollup catch-all row (the "other" key or country "--"): it is
// shown as "others", never linked and never counted as one client or app.
export interface Row { other?: boolean }

export interface TopClient extends Row {
  mac: string; name: string; down: number; up: number; topApp: App | null; countries: string[] | null;
}
export interface TopApp extends App, Row { clients: number; down: number; up: number }
export interface Country extends Row { cc: string; org: string; down: number; up: number; share: number }

export interface Overview {
  wan: Wan;
  // new24h is null until the router has watched for a day.
  clients: { online: number; new24h: number | null };
  lostEvents?: number;
  series: SeriesPoint[];
  topClients: TopClient[];
  topApps: TopApp[];
  countries: Country[];
  liveFlows: number;
}

export interface Link { kind: string; iface: string; ssid: string; band: string; signal: number }

export interface Device {
  mac: string; name: string; hostname: string; vendor: string; label: string; group: string;
  random: boolean; ipv4: string[] | null; ipv6: string[] | null; link: Link;
  first: string; last: string; online: boolean;
  rate: { up: number; down: number };
  last24h: { down: number; up: number };
  topApp: App | null; countryCount: number;
  /** Isolation (spec 6.7) and quarantine (6.8): set by the router when the rules run. */
  isolate?: boolean; quarantined?: boolean; likelySame?: string;
  isolation?: { routed: boolean; bridged: boolean; ssid: string; ssidIsolated: boolean; hardwareSwitch: boolean; text: string };
}

export interface Domain extends Row { name: string; src: string; down: number; up: number }
export interface Place extends Row { cc: string; org: string; down: number; up: number }
export interface AppUsage extends Row {
  app: App; down: number; up: number; flows: number; share: number;
  domains: Domain[] | null; places: Place[] | null;
}

export interface Destinations {
  countries: ({ cc: string; down: number; up: number } & Row)[];
  orgs: ({ asn: string; org: string; down: number; up: number } & Row)[];
  domains: ({ name: string; app: App | null; down: number; up: number } & Row)[];
}

export interface FlowRow {
  end: string; start: string; mac: string; client: string; dir: string;
  local: string; remote: string; proto: number; name: string; nameSrc: string;
  app: App; cc: string; asn: number; org: string; up: number; down: number; evicted: boolean;
}
export interface FlowsPage { rows: FlowRow[]; next: string }

// GET /api/devices/{mac}/live: what a device is doing now. Every rate is bytes
// per second over the flow table's last Sync; stepSec is that interval (2 s
// while somebody watches, longer otherwise). Go's empty lists arrive as null.
export interface LiveApp {
  app: App; down: number; up: number; flows: number;
  domain: string; src: string; cc: string; org: string;
  /** Down rate at the ring's newest points, oldest first. */
  spark: number[] | null;
}
export interface LiveConn {
  app: App; name: string; src: string; cc: string; org: string; asn: number;
  proto: number; port: number; down: number; up: number; bytesDown: number; bytesUp: number; durSec: number;
  /** The two ends as address:port, the device's first: what the cut route takes. */
  local: string; remote: string;
}
export interface LivePlace extends Row { cc: string; org: string; down: number; up: number }
/** One Sync of the ring: bytes per second per app over the stepSec before t. */
export interface LivePoint { t: string; stepSec: number; down: Record<string, number> | null; up: Record<string, number> | null }
export interface Live {
  at: string; stepSec: number; rate: { down: number; up: number }; flows: number;
  apps: LiveApp[] | null; conns: LiveConn[] | null; places: LivePlace[] | null; series: LivePoint[] | null;
}

/** A group of devices, as GET /api/groups lists it. */
export interface Group {
  id: string;
  name: string;
  members: string[];
}

// The house-wide views (GET /api/apps, /api/apps/{id}, /api/destinations,
// /api/destinations/{cc}), mirrored from internal/api/house.go.
export interface ClientRef extends Row { mac: string; name: string; down: number; up: number }
export interface HouseApp extends Row {
  app: App; down: number; up: number; flows: number; share: number;
  /** How many clients used it; top names the busiest few. */
  clients: number; top: ClientRef[];
}
export interface Network extends Row { cc: string; asn: string; org: string; down: number; up: number }
export interface AppDetail {
  app: App; down: number; up: number; flows: number; share: number;
  /** Every client of the app; clients lists the busiest 50. */
  clientCount: number; clients: ClientRef[]; places: Network[]; domains: Domain[]; series: SeriesPoint[];
}
export interface HouseCountry extends Row { cc: string; down: number; up: number; share: number; clients: number; top: ClientRef[] }
export interface HouseDestinations {
  countries: HouseCountry[];
  networks: Network[];
  domains: ({ name: string; app: App | null; down: number; up: number } & Row)[];
}
export interface CountryDetail {
  cc: string; down: number; up: number; share: number;
  clientCount: number; clients: ClientRef[]; apps: ({ app: App; down: number; up: number } & Row)[]; networks: Network[]; series: SeriesPoint[];
}
