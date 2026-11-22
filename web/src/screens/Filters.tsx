// DNS filters: the house's DNS goes through the router, DoT and DoH can be
// stopped, groups can get safe search and YouTube's restricted mode from the
// router's second resolver, and adblock-fast's lists are switched here. Each
// control states what it cannot do beside it.

import { h } from "preact";
import { useState } from "preact/hooks";
import { getJSON, sendJSON, usePoll } from "../api";
import { Header, Status, Loading, Pills, useSession } from "../ui";
import { filtersBody, safeDnsText, type Adblock, type Filters, type YouTubeLevel } from "../filters";

interface Group { id: string; name: string; members: string[]; filteredDns?: boolean }

export function FiltersScreen() {
  const { canWrite } = useSession();
  const [n, setN] = useState(0);
  const refresh = () => setN((x) => x + 1);
  const filters = usePoll((s) => getJSON<Filters>("/api/filters", s), 30_000, [n]);
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 0, [n]);
  const adblock = usePoll((s) => getJSON<Adblock>("/api/adblock", s), 0, [n]);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const save = async (fn: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    setErr("");
    try {
      await fn();
      refresh();
    } catch (x) {
      setErr(x instanceof Error ? x.message : String(x));
    } finally {
      setBusy(false);
    }
  };
  const f = filters.data;
  const put = (o: Partial<Filters>) => f && save(() => sendJSON("PUT", "/api/filters", filtersBody({ ...f, ...o })));
  const toggle = (label: string, on: boolean, change: (v: boolean) => void, note?: string) => (
    <label class="check">
      <input type="checkbox" checked={on} disabled={!canWrite || busy} onChange={() => change(!on)} />
      <span>{label}</span>
      {note && <span class="small muted"> {note}</span>}
    </label>
  );

  return (
    <div class="page">
      <Header section="rules" />
      <main class="main">
        <div class="toolbar">
          <h1>DNS filters</h1>
          <span class="muted small">how the house looks up names; app and country rules are under Rules</span>
          <a class="btn btn-ghost push" href="#/rules">Rules</a>
        </div>
        <Status polled={filters} />
        {err && <p class="banner" role="alert">{err}</p>}
        {!f ? (filters.error ? null : <Loading />) : (
          <div class="stack">
            <section class="card">
              <h2>Whole house</h2>
              <div class="checks">
                {toggle("All DNS through the router", f.forceDns, (v) => put({ forceDns: v }), "the router answers a query sent to any DNS server")}
                {toggle("Block DNS over TLS", f.blockDot, (v) => put({ blockDot: v }), "port 853")}
                {toggle("Block DNS over HTTPS", f.blockDoh, (v) => put({ blockDoh: v }), "a DoH server the catalog does not know is not blocked this way")}
              </div>
              <p class="small muted">VPN is blocked as a category under Rules; a VPN on an unusual port is not blocked this way.</p>
            </section>
            <section class="card">
              <h2>Safe search for filtered groups</h2>
              <div class="checks">
                {toggle("Google and Bing safe search", f.safeSearch, (v) => put({ safeSearch: v }))}
              </div>
              <Pills
                label="YouTube"
                inset
                value={f.youtube}
                onPick={(v) => canWrite && put({ youtube: v as YouTubeLevel })}
                items={[{ id: "off", label: "YouTube: off" }, { id: "moderate", label: "moderate" }, { id: "strict", label: "strict" }]}
              />
              <p class={f.safeDns?.healthy ? "small" : "small muted"}>{safeDnsText(f)}</p>
              <div class="checks">
                {(groups.data ?? []).length === 0 && <span class="small muted">No groups yet: the filter works on the groups that have it turned on.</span>}
                {(groups.data ?? []).map((g) => (
                  <label key={g.id} class="check">
                    <input
                      type="checkbox"
                      checked={!!g.filteredDns}
                      disabled={!canWrite || busy}
                      onChange={() => save(() => sendJSON("PUT", `/api/groups/${encodeURIComponent(g.id)}`, { name: g.name, filteredDns: !g.filteredDns }))}
                    />
                    <span>DNS filter: {g.name}</span>
                  </label>
                ))}
              </div>
            </section>
            <section class="card">
              <h2>adblock-fast lists</h2>
              {!adblock.data ? <Loading /> : !adblock.data.installed ? (
                <p class="small muted">adblock-fast is not installed.</p>
              ) : (
                <div class="checks">
                  {adblock.data.sources.map((src) => (
                    <label key={src.id} class="check">
                      <input
                        type="checkbox"
                        checked={src.enabled}
                        disabled={!canWrite || busy}
                        onChange={() => save(() => sendJSON("PUT", `/api/adblock/${encodeURIComponent(src.id)}`, { url: src.url, enabled: !src.enabled }))}
                      />
                      <span>{src.name || src.url}</span>
                    </label>
                  ))}
                  <span class="small muted">{adblock.data.restartNote}.</span>
                </div>
              )}
            </section>
          </div>
        )}
      </main>
    </div>
  );
}
