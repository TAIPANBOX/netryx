// The console's entry: a hash router over the screens and the period every
// screen shares. Routes are #/, #/clients, #/client/<mac>, #/flows, #/rules.
// Behind them is the login: with no session, or after a 401 anywhere, the
// login takes the page and gives it back at the screen it interrupted.

import { h, render } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { Range } from "./types";
import type { IconManifest } from "./icons";
import { getJSON, getSession, onUnauthorized } from "./api";
import { IconContext, SessionContext, type SessionInfo } from "./ui";
import { parseRoute, RANGES, WINDOWS, type Win } from "./query";
import { nextLogin } from "./rules";
import { Overview } from "./screens/Overview";
import { Clients } from "./screens/Clients";
import { Client } from "./screens/Client";
import { Flows } from "./screens/Flows";
import { FiltersScreen } from "./screens/Filters";
import { Security } from "./screens/Security";
import { Rules } from "./screens/Rules";
import { Login } from "./screens/Login";
import { Apps, AppScreen } from "./screens/Apps";
import { Destinations, Destination } from "./screens/Destinations";
import { Network } from "./screens/Network";

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
  // The screen a missing session interrupted: where the login returns to.
  const back = useRef("#/");

  useEffect(() => {
    const off = onUnauthorized(() => {
      back.current = nextLogin(location.hash);
      setAuth({ state: "login" });
    });
    getSession().then(
      (s) => {
        if (s) setAuth({ state: "in", info: { user: s.user, canWrite: !!s.canWrite && !s.noRPC, noRPC: !!s.noRPC } });
        else {
          back.current = nextLogin(location.hash);
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
        <Screen />
      </IconContext.Provider>
    </SessionContext.Provider>
  );
}

function Screen() {
  const [hash, setHash] = useState(location.hash);
  const [range, setRange] = useState<Range>(savedRange);
  useEffect(() => {
    const on = () => {
      setHash(location.hash);
      window.scrollTo(0, 0);
    };
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
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

  const route = parseRoute(hash);
  switch (route.name) {
    case "clients":
      return <Clients win={win} onWin={pickWin} />;
    case "client":
      return <Client key={route.mac} mac={route.mac} range={range} onRange={pick} win={win} onWin={pickWin} />;
    case "flows":
      return <Flows key={route.q} initial={route.q} win={win} onWin={pickWin} />;
    case "rules":
      return <Rules />;
    case "filters":
      return <FiltersScreen />;
    case "security":
      return <Security />;
    case "apps":
      return <Apps range={range} onRange={pick} win={win} onWin={pickWin} />;
    case "app":
      return <AppScreen key={route.id} id={route.id} range={range} onRange={pick} />;
    case "destinations":
      return <Destinations range={range} onRange={pick} win={win} onWin={pickWin} />;
    case "network":
      return <Network />;
    case "destination":
      return <Destination key={route.cc} cc={route.cc} range={range} onRange={pick} />;
    default:
      return <Overview range={range} onRange={pick} />;
  }
}

// The boot line is plain text in index.html; it is cleared, not diffed, so
// it cannot linger above the first screen.
const root = document.getElementById("app")!;
root.textContent = "";
render(<App />, root);
