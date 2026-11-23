// The console's entry: a hash router over the screens and the period every
// screen shares. Routes are #/, #/clients, #/client/<mac>, #/flows, #/rules.
// Behind them is the login: with no session, or after a 401 anywhere, the
// login takes the page and gives it back at the screen it interrupted.

import { h, render } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Range } from "./types";
import type { IconManifest } from "./icons";
import { getJSON, getSession, handoffCode, loginWithLuCI, noteUse, onUnauthorized, wentIdle } from "./api";
import { IconContext, SessionContext, SpanContext, type SessionInfo } from "./ui";
import { parseSpan } from "./span";
import { parseRoute, RANGES, WINDOWS, type Win } from "./query";
import { nextLogin } from "./rules";
import { lazy } from "./lazy";
import { Overview } from "./screens/Overview";
import { Clients } from "./screens/Clients";
const Client360 = lazy(() => import("./screens/Client360").then((m) => m.Client360));
const Flows = lazy(() => import("./screens/Flows").then((m) => m.Flows));
const FiltersScreen = lazy(() => import("./screens/Filters").then((m) => m.FiltersScreen));
const Security = lazy(() => import("./screens/Security").then((m) => m.Security));
const Rules = lazy(() => import("./screens/Rules").then((m) => m.Rules));
import { Login } from "./screens/Login";
const Apps = lazy(() => import("./screens/Apps").then((m) => m.Apps));
const AppScreen = lazy(() => import("./screens/Apps").then((m) => m.AppScreen));
const Destinations = lazy(() => import("./screens/Destinations").then((m) => m.Destinations));
const Destination = lazy(() => import("./screens/Destinations").then((m) => m.Destination));
const Network = lazy(() => import("./screens/Network").then((m) => m.Network));
const NetworkScreen = lazy(() => import("./screens/Entities").then((m) => m.NetworkScreen));
const DomainScreen = lazy(() => import("./screens/Entities").then((m) => m.DomainScreen));
const GroupScreen = lazy(() => import("./screens/Entities").then((m) => m.GroupScreen));
import { CardsProvider } from "./screens/windows";
import { SearchBox } from "./screens/Search";
const CompareScreen = lazy(() => import("./screens/Compare").then((m) => m.CompareScreen));
const PeopleScreen = lazy(() => import("./screens/People").then((m) => m.PeopleScreen));
const PersonScreen = lazy(() => import("./screens/People").then((m) => m.PersonScreen));
import { HoverCards } from "./screens/HoverCards";

// The period is a per-viewer convenience; storage may be missing (a private
// window, a blocked site), and then the page simply starts at 24 hours.
function savedRange(): Range {
  try {
    const v = localStorage.getItem("netryx.range");
    if (RANGES.some((r) => r.id === v)) return v as Range;
  } catch {
    // no storage: fall through to the default
  }
  return "24h";
}

// The window the Now columns average over, remembered as the period is.
function savedWindow(): Win {
  try {
    const v = localStorage.getItem("netryx.window");
    if (WINDOWS.some((w) => w.id === v)) return v as Win;
  } catch {
    // no storage: the default
  }
  return "3m";
}

// The icon manifest is loaded once for the whole page. Without it (no icon
// pack in the build, or the request failed) every app is a letter tile.
function useIcons(): IconManifest | null {
  const [icons, setIcons] = useState<IconManifest | null>(null);
  useEffect(() => {
    getJSON<IconManifest>("/icons/manifest.json").then(
      (m) => setIcons(m && typeof m === "object" && !Array.isArray(m) ? m : null),
      () => setIcons(null),
    );
  }, []);
  return icons;
}

type Auth = { state: "checking" } | { state: "login" } | { state: "down"; text: string } | { state: "in"; info: SessionInfo };

function App() {
  const [auth, setAuth] = useState<Auth>({ state: "checking" });
  // Why the login shows when LuCI's link did not open the console.
  const [note, setNote] = useState("");
  // The screen a missing session interrupted: where the login returns to.
  const back = useRef("#/");

  useEffect(() => {
    const off = onUnauthorized(() => {
      back.current = nextLogin(location.hash);
      if (wentIdle) setNote("You were signed out after a while without using the page.");
      setAuth({ state: "login" });
    });
    // A person at the page: only this keeps the session alive.
    const use = () => noteUse();
    const seen = () => document.visibilityState === "visible" && noteUse();
    const kinds = ["pointerdown", "pointermove", "keydown", "wheel", "focus"];
    for (const k of kinds) window.addEventListener(k, use, { passive: true });
    document.addEventListener("visibilitychange", seen);
    // Opened from LuCI: the one-time code leaves the address and the history
    // at once, before anything else runs, and is redeemed for a session.
    const code = handoffCode(location.hash);
    if (code) {
      history.replaceState(null, "", location.pathname + location.search + "#/");
      loginWithLuCI(code).then(async (r) => {
        if (r.ok) return setAuth({ state: "in", info: { user: r.user, canWrite: r.canWrite, noRPC: false } });
        // A code that does not open (another browser's, or spent) leaves a
        // session this browser already has as it is.
        const s = await getSession().catch(() => null);
        if (s) return setAuth({ state: "in", info: { user: s.user, canWrite: !!s.canWrite && !s.noRPC, noRPC: !!s.noRPC } });
        setNote(r.text);
        setAuth({ state: "login" });
      });
      return off;
    }
    getSession().then(
      (s) => {
        if (s) setAuth({ state: "in", info: { user: s.user, canWrite: !!s.canWrite && !s.noRPC, noRPC: !!s.noRPC } });
        else {
          back.current = nextLogin(location.hash);
          if (wentIdle) setNote("You were signed out after a while without using the page.");
          setAuth({ state: "login" });
        }
      },
      (e) => setAuth({ state: "down", text: e instanceof Error ? e.message : String(e) }),
    );
    return off;
  }, []);

  // The session is read again every 15 s while the page is shown: a rpcd that
  // came back turns "view only" into a login, and the token and rights stay current.
  useEffect(() => {
    if (auth.state !== "in") return;
    const id = window.setInterval(() => {
      if (document.visibilityState === "hidden") return;
      getSession().then(
        (s) => {
          if (!s) {
            back.current = nextLogin(location.hash);
            if (wentIdle) setNote("You were signed out after a while without using the page.");
            setAuth({ state: "login" });
          } else {
            const info = { user: s.user, canWrite: !!s.canWrite && !s.noRPC, noRPC: !!s.noRPC };
            setAuth((a) => (a.state === "in" && (a.info.user !== info.user || a.info.canWrite !== info.canWrite || a.info.noRPC !== info.noRPC) ? { state: "in", info } : a));
          }
        },
        () => {
          // the router is not answering: the screens say so themselves
        },
      );
    }, 15_000);
    return () => window.clearInterval(id);
  }, [auth.state]);

  if (auth.state === "checking") return <p class="boot">Loading…</p>;
  if (auth.state === "down") return <p class="banner banner-top" role="status">The router is not answering: {auth.text}. Reload the page.</p>;
  if (auth.state === "login") {
    return (
      <Login
        note={note}
        onDone={(user, canWrite) => {
          if (location.hash !== back.current) location.hash = back.current;
          setAuth({ state: "in", info: { user, canWrite, noRPC: false } });
        }}
      />
    );
  }
  return <Signed info={auth.info} />;
}

function Signed({ info }: { info: SessionInfo }) {
  const icons = useIcons();
  return (
    <SessionContext.Provider value={info}>
      <IconContext.Provider value={icons}>
        <CardsProvider>
          <Screen />
          <SearchBox />
          <HoverCards />
        </CardsProvider>
      </IconContext.Provider>
    </SessionContext.Provider>
  );
}

function Screen() {
  const [hash, setHash] = useState(location.hash);
  // A screen writes its search into the address with replaceState, which
  // fires no hashchange, so hash can lag the address. Every real navigation
  // bumps nav, so a nav link back to the same screen starts it afresh.
  const [nav, setNav] = useState(0);
  const [range, setRange] = useState<Range>(savedRange);
  useEffect(() => {
    const on = () => {
      setHash(location.hash);
      setNav((n) => n + 1);
      window.scrollTo(0, 0);
    };
    // A stretch picked on a chart changes the address without starting the screen afresh.
    const span = () => setHash(location.hash);
    window.addEventListener("hashchange", on);
    window.addEventListener("netryx-span", span);
    return () => {
      window.removeEventListener("hashchange", on);
      window.removeEventListener("netryx-span", span);
    };
  }, []);
  const [win, setWin] = useState<Win>(savedWindow);
  const pickWin = (w: Win) => {
    setWin(w);
    try {
      localStorage.setItem("netryx.window", w);
    } catch {
      // not remembered, still applied
    }
  };
  const pick = (r: Range) => {
    setRange(r);
    try {
      localStorage.setItem("netryx.range", r);
    } catch {
      // not remembered, still applied
    }
  };

  return (
    <SpanContext.Provider value={parseSpan(hash)}>
      <Route hash={hash} nav={nav} range={range} pick={pick} win={win} pickWin={pickWin} />
    </SpanContext.Provider>
  );
}

function Route({ hash, nav, range, pick, win, pickWin }: { hash: string; nav: number; range: Range; pick: (r: Range) => void; win: Win; pickWin: (w: Win) => void }) {
  const route = parseRoute(hash);
  switch (route.name) {
    case "clients":
      return <Clients key={`${route.q ?? ""}|${nav}`} q0={route.q ?? ""} win={win} onWin={pickWin} />;
    case "client":
      return <Client360 key={route.mac} mac={route.mac} from={route.from ?? ""} range={range} onRange={pick} win={win} onWin={pickWin} />;
    case "flows":
      return <Flows key={hash} initial={route.q} from={route.from ?? ""} to={route.to ?? ""} asn={new URLSearchParams(hash.split("?")[1] ?? "").get("asn") ?? ""} win={win} onWin={pickWin} />;
    case "rules":
      return <Rules focus={route.rule ?? ""} />;
    case "filters":
      return <FiltersScreen />;
    case "security":
      return <Security />;
    case "apps":
      return <Apps key={`${route.cat ?? ""}|${nav}`} cat0={route.cat ?? ""} q0={route.q ?? ""} range={range} onRange={pick} win={win} onWin={pickWin} />;
    case "app":
      return <AppScreen key={route.id} id={route.id} range={range} onRange={pick} />;
    case "destinations":
      return <Destinations range={range} onRange={pick} win={win} onWin={pickWin} />;
    case "network":
      return <Network />;
    case "asn":
      return <NetworkScreen key={route.asn} asn={route.asn} from={route.from} range={range} onRange={pick} />;
    case "domain":
      return <DomainScreen key={route.domain} domain={route.domain} from={route.from} range={range} onRange={pick} />;
    case "people":
      return <PeopleScreen />;
    case "person":
      return <PersonScreen key={route.id} id={route.id} />;
    case "compare":
      return <CompareScreen macs={route.macs} win={win} onWin={pickWin} />;
    case "group":
      return <GroupScreen key={route.id} id={route.id} from={route.from} />;
    case "destination":
      return <Destination key={route.cc} cc={route.cc} range={range} onRange={pick} />;
    default:
      return <Overview range={range} onRange={pick} win={win} onWin={pickWin} />;
  }
}

// The boot line is plain text in index.html; it is cleared, not diffed, so
// it cannot linger above the first screen.
const root = document.getElementById("app")!;
root.textContent = "";
render(<App />, root);
