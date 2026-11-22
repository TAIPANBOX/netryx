// The search: Cmd+K (Ctrl+K) anywhere, or "/" outside a text field, opens a
// box that finds any client, app, category, country, network, domain, group
// or rule by its name, address, MAC or AS number. Enter opens the thing.

import { h } from "preact";
import { useContext, useEffect, useRef, useState } from "preact/hooks";
import type { Device, Group, HouseApp, HouseDestinations } from "../types";
import type { Rule } from "../rules";
import { manifestName } from "../rules";
import { getJSON } from "../api";
import { IconContext } from "../ui";
import { buildIndex, searchHits, type Hit } from "../search";

const OPEN = "netryx-search";

/** Opens the search box, as the header's button does. */
export function openSearch() {
  window.dispatchEvent(new Event(OPEN));
}

/** Whether a key press belongs to a text field, where "/" is a character. */
function typing(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  return !!el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT" || el.isContentEditable);
}

const KIND_TEXT: Record<string, string> = {
  client: "Client", app: "App", category: "Category", country: "Country", network: "Network", domain: "Domain", group: "Group", rule: "Rule",
};

export function SearchBox() {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const [index, setIndex] = useState<Hit[] | null>(null);
  const [error, setError] = useState("");
  const [sel, setSel] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const appName = manifestName(useContext(IconContext));

  useEffect(() => {
    const show = () => setOpen(true);
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((o) => !o);
      } else if (e.key === "/" && !e.metaKey && !e.ctrlKey && !e.altKey && !typing(e.target)) {
        e.preventDefault();
        setOpen(true);
      }
    };
    window.addEventListener(OPEN, show);
    window.addEventListener("keydown", key);
    return () => {
      window.removeEventListener(OPEN, show);
      window.removeEventListener("keydown", key);
    };
  }, []);

  // The index is read afresh each time the box opens: five small reads.
  useEffect(() => {
    if (!open) return;
    setQ("");
    setSel(0);
    setError("");
    input.current?.focus();
    const ac = new AbortController();
    Promise.all([
      getJSON<Device[]>("/api/devices", ac.signal),
      getJSON<HouseApp[]>("/api/apps?range=24h", ac.signal),
      getJSON<HouseDestinations>("/api/destinations?range=24h", ac.signal),
      getJSON<Group[]>("/api/groups", ac.signal).catch(() => [] as Group[]),
      getJSON<Rule[]>("/api/rules", ac.signal).catch(() => [] as Rule[]),
    ])
      .then(([devices, apps, dest, groups, rules]) => setIndex(buildIndex({ devices, apps, dest, groups, rules, appName })))
      .catch((e: Error) => {
        if (!ac.signal.aborted) setError(e.message);
      });
    return () => ac.abort();
  }, [open]);

  if (!open) return null;
  const hits = searchHits(q, index ?? []);
  const go = (h: Hit | undefined) => {
    if (!h) return;
    setOpen(false);
    location.hash = h.href;
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      setSel((s) => Math.min(hits.length - 1, s + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSel((s) => Math.max(0, s - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      go(hits[sel]);
    }
  };
  return (
    <div class="search-veil" onClick={(e) => e.target === e.currentTarget && setOpen(false)}>
      <div class="search-box" role="dialog" aria-label="Search">
        <input
          ref={input}
          class="input search-input"
          type="search"
          placeholder="Find a client, app, country, network, domain, group or rule"
          aria-label="Search"
          aria-controls="search-hits"
          value={q}
          onInput={(e) => {
            setQ((e.target as HTMLInputElement).value);
            setSel(0);
          }}
          onKeyDown={onKey}
        />
        <ul id="search-hits" class="search-hits" role="listbox">
          {error && <li class="bad-text small">The router is not answering: {error}.</li>}
          {!error && !index && <li class="muted small">Loading…</li>}
          {!error && index && q.trim() && hits.length === 0 && <li class="muted small">Nothing found.</li>}
          {hits.map((h, i) => (
            <li key={h.href} role="option" aria-selected={i === sel} class={i === sel ? "search-hit active" : "search-hit"} onMouseEnter={() => setSel(i)} onClick={() => go(h)}>
              <span class="tag">{KIND_TEXT[h.kind] ?? h.kind}</span>
              <span class="col-tight">
                <span class="strong ellipsis">{h.label}</span>
                <span class="muted small ellipsis">{h.sub}</span>
              </span>
            </li>
          ))}
        </ul>
        <p class="muted small search-help">↑ ↓ to choose · Enter to open · Esc to close</p>
      </div>
    </div>
  );
}
