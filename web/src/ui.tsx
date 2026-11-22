// Pieces every screen shares: the header with its navigation and period, pill
// groups, cards, country badges, app icons and the line that says the router
// is not answering.

import { h, Fragment, createContext, type ComponentChildren } from "preact";
import { useContext, useState } from "preact/hooks";
import type { Range } from "./types";
import type { Polled } from "./api";
import { RANGES } from "./query";
import { logout } from "./api";
import { time } from "./format";
import { appIcon, type IconManifest } from "./icons";

type Section = "overview" | "clients" | "apps" | "destinations" | "flows" | "rules" | "security";

// The sections the mockup names that this stage does not have yet are shown
// greyed out, so the layout matches and nothing pretends to work.
const NAV: { id: Section | null; label: string; href?: string }[] = [
  { id: "overview", label: "Overview", href: "#/" },
  { id: "clients", label: "Clients", href: "#/clients" },
  { id: "apps", label: "Apps", href: "#/apps" },
  { id: "destinations", label: "Destinations", href: "#/destinations" },
  { id: "flows", label: "Flows", href: "#/flows" },
  { id: "security", label: "Security", href: "#/security" },
  { id: "rules", label: "Rules", href: "#/rules" },
  { id: null, label: "Network" },
];

/** Who is signed in and what they may do; the page sets it once /api/session has answered. */
export interface SessionInfo { user: string; canWrite: boolean; noRPC: boolean }
export const SessionContext = createContext<SessionInfo>({ user: "", canWrite: false, noRPC: false });
export const useSession = () => useContext(SessionContext);

export const LATER = "Coming in a later stage";

function Logo() {
  return (
    <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#4C9AFF" stroke-width="2" aria-hidden="true">
      <circle cx="12" cy="12" r="3" />
      <circle cx="4" cy="6" r="2" />
      <circle cx="20" cy="6" r="2" />
      <circle cx="12" cy="21" r="2" />
      <path d="M6 7l4 3M18 7l-4 3M12 15v4" />
    </svg>
  );
}

export function Header(p: { section: Section; range?: Range; onRange?: (r: Range) => void; note?: string; pills?: ComponentChildren }) {
  const s = useSession();
  return (
    <Fragment>
    <header class="top">
      <div class="brand">
        <Logo />
        netryx
      </div>
      <nav aria-label="Sections" class="nav">
        {NAV.map((n) =>
          n.href ? (
            <a key={n.label} href={n.href} class={n.id === p.section ? "nav-link active" : "nav-link"} aria-current={n.id === p.section ? "page" : undefined}>
              {n.label}
            </a>
          ) : (
            <span key={n.label} class="nav-link nav-later" aria-disabled="true" title={LATER}>
              {n.label}
            </span>
          ),
        )}
      </nav>
      {p.pills ? (
        p.pills
      ) : p.range && p.onRange ? (
        <Pills label="Period" items={RANGES.map((r) => ({ id: r.id, label: r.label }))} value={p.range} onPick={(v) => p.onRange!(v as Range)} />
      ) : p.note ? (
        <span class="muted small">{p.note}</span>
      ) : null}
      {s.user && (
        <span class="who small">
          <span class="muted">{s.user}</span>
          {!s.canWrite && <span class="tag">read only</span>}
          <button type="button" class="btn-mini" onClick={() => void logout()}>Sign out</button>
        </span>
      )}
    </header>
    {s.noRPC && (
      <p class="banner banner-top" role="status">
        Sign-in unavailable: rpcd is not answering. Read only, from the router itself.
      </p>
    )}
    </Fragment>
  );
}

export function Pills(p: { label: string; items: { id: string; label: string }[]; value: string; onPick: (id: string) => void; inset?: boolean }) {
  return (
    <div role="group" aria-label={p.label} class={p.inset ? "pills pills-inset" : "pills"}>
      {p.items.map((it) => (
        <button key={it.id} type="button" class={it.id === p.value ? "pill active" : "pill"} aria-pressed={it.id === p.value} onClick={() => p.onPick(it.id)}>
          {it.label}
        </button>
      ))}
    </div>
  );
}

export function Card(p: { label: string; title?: string; aside?: ComponentChildren; children?: ComponentChildren; class?: string }) {
  return (
    <section aria-label={p.label} class={p.class ? `card ${p.class}` : "card"}>
      {(p.title || p.aside) && (
        <div class="card-head">
          {p.title && <h2>{p.title}</h2>}
          {p.aside}
        </div>
      )}
      {p.children}
    </section>
  );
}

/** A country code as a badge: no flag images or emoji, nothing to fetch. */
export function CC({ cc }: { cc: string }) {
  return <span class="cc">{cc || "--"}</span>;
}

/** Says when the router stopped answering, and how old the figures shown are. */
export function Status<T>({ polled }: { polled: Polled<T> }) {
  if (!polled.error) return null;
  return (
    <p class="banner" role="status">
      The router is not answering: {polled.error}.
      {polled.at ? ` Showing data from ${time(new Date(polled.at).toISOString())}.` : ""}
    </p>
  );
}

export function Loading() {
  return <p class="muted">Loading…</p>;
}

export function Empty({ children }: { children: ComponentChildren }) {
  return <p class="empty">{children}</p>;
}

/**
 * Icon URLs that failed to load (a favicon the router has not fetched yet),
 * kept for the page's life so another screen draws the letter at once
 * instead of asking again.
 */
export const brokenIcons = new Set<string>();
export function iconBroke(src: string, setFailed: (s: string) => void) {
  brokenIcons.add(src);
  setFailed(src);
}

/** The icon manifest, loaded once by the page; null until then, or without an icon pack. */
export const IconContext = createContext<IconManifest | null>(null);

/**
 * An app's tile: its bundled SVG drawn white (or dark on a light brand colour),
 * the favicon the router fetched, or its first letter. An image that fails to
 * load falls back to the letter, so a missing icon never shows as broken.
 * Images come only from /icons/ on the console; the name is beside the tile,
 * so the tile itself is hidden from screen readers.
 */
export function AppIcon({ id, name, size = 34 }: { id: string; name: string; size?: number }) {
  const i = appIcon(id, useContext(IconContext), name);
  const [failed, setFailed] = useState("");
  const showImg = !!i.src && failed !== i.src && !brokenIcons.has(i.src);
  const box = { width: `${size}px`, height: `${size}px`, borderRadius: `${Math.round(size / 4)}px`, background: i.color };
  const glyph = Math.round(size * (i.kind === "svg" ? 0.56 : 0.66));
  return (
    <span class={i.dark ? "app-icon dark" : "app-icon"} style={box} aria-hidden="true">
      {showImg ? (
        <img
          src={i.src}
          alt=""
          width={glyph}
          height={glyph}
          decoding="async"
          class={i.kind === "svg" ? "app-glyph" : "app-fav"}
          onError={() => iconBroke(i.src ?? "", setFailed)}
        />
      ) : (
        <span class="app-letter" style={{ fontSize: `${Math.round(size * 0.46)}px` }}>{i.letter}</span>
      )}
    </span>
  );
}
