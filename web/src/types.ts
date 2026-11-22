import type { HourSpark } from "./live";
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
export interface Country extends Row { cc: string; org: string; asn?: string; down: number; up: number; share: number }

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
  /** The average over the window asked for (bytes per second), and the seconds it covers. */
  recent?: { up: number; down: number }; recentSec?: number;
  last24h: { down: number; up: number };
  /** The last hour in steps, on the client list only. */
  spark?: HourSpark;
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
  /** The average over the window asked for, bytes per second. */
  recent?: { up: number; down: number };
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
  /** What a live flow moved over the window asked for. */
  winUp?: number; winDown?: number;
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
export interface LivePlace extends Row { cc: string; org: string; asn?: number; down: number; up: number }
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
  recent?: { up: number; down: number };
  /** The last hour in steps; none on the folded row. */
  spark?: HourSpark;
}
export interface Network extends Row { cc: string; asn: string; org: string; down: number; up: number }
export interface AppDetail {
  app: App; down: number; up: number; flows: number; share: number;
  /** Every client of the app; clients lists the busiest 50. */
  clientCount: number; clients: ClientRef[]; places: Network[]; domains: Domain[]; series: SeriesPoint[];
}
export interface HouseCountry extends Row { cc: string; down: number; up: number; share: number; clients: number; top: ClientRef[]; recent?: { up: number; down: number } }
export interface HouseDestinations {
  countries: HouseCountry[];
  networks: Network[];
  domains: ({ name: string; app: App | null; down: number; up: number } & Row)[];
}
export interface CountryDetail {
  cc: string; down: number; up: number; share: number;
  clientCount: number; clients: ClientRef[]; apps: ({ app: App; down: number; up: number } & Row)[]; networks: Network[]; series: SeriesPoint[];
}

// GET /api/network, mirrored from internal/api/network.go and internal/netview.
// A section the router lacks carries `why`; `note` says what stops it showing more.
export interface NetIface {
  name: string; proto: string; device: string; up: boolean; wan: boolean; uptimeSec: number;
  ipv4: string[] | null; ipv6: string[] | null; gateway: string; rxBytes: number; txBytes: number;
}
export interface NetDevice { mac: string; name: string; online: boolean; signal?: number; band?: string }
export interface NetPort { name: string; bridge: string; up: boolean; speedMbps: number; switch: boolean; devices: NetDevice[] }
export interface NetRadio { name: string; band: string; channel: string; width: string; disabled: boolean }
export interface NetAP { name: string; radio: string; ssid: string; mode: string; network: string; disabled: boolean }
export interface NetLiveAP { iface: string; ssid: string; band: string; channel: number; clients: NetDevice[] }
export interface NetPeer { name: string; publicKey: string; endpoint: string; allowedIps: string[] | null; handshake: string | null; rx: number; tx: number }
export interface NetWG { name: string; listenPort: number; addresses: string[]; publicKey: string; up: boolean; peers: NetPeer[] }
export interface NetForward { name: string; from: string; proto: string; port: string; destIp: string; destPort: string; enabled: boolean; device: NetDevice | null }
export interface NetworkView {
  interfacesWhy: string; interfaces: NetIface[]; ports: NetPort[];
  wifi: { why: string; note: string; radios: NetRadio[]; aps: NetAP[]; live: NetLiveAP[] };
  wireguard: { why: string; note: string; interfaces: NetWG[] };
  forwards: { why: string; rules: NetForward[] };
}

// GET /api/networks/{asn} and /api/domains/{name}, mirrored from internal/api/entity.go.
export interface EntityClient { mac: string; name: string; down: number; up: number }
export interface NetworkPage {
  asn: string; org: string; down: number; up: number;
  countries: { cc: string; down: number; up: number }[];
  clients: EntityClient[];
  apps: ({ app: App; down: number; up: number } & Row)[];
}
export interface DomainPage {
  name: string; down: number; up: number;
  apps: { app: App; src: string; down: number; up: number }[];
  clients: EntityClient[];
}
