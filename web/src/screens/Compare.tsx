// Two or three clients side by side, from #/compare?mac=..&mac=..: the
// figures that tell them apart, and each one's last hour.

import { h } from "preact";
import type { Destinations, Device, Group } from "../types";
import { getJSON, mac as enc, usePoll } from "../api";
import { windowLabel, type Win } from "../query";
import { Header, Status, Loading, Empty, CC } from "../ui";
import { HourSparkline } from "../charts";
import { countryName, groupName } from "../format";
import { compareHash, compareRows } from "../compare";
import { ChipChain, ClientLink, EntityLink } from "./entity";
import { deviceName } from "./Clients";

export function CompareScreen({ macs, win, onWin }: { macs: string[]; win: Win; onWin: (w: Win) => void }) {
  const polled = usePoll((s) => getJSON<Device[]>(`/api/devices?window=${win}`, s), 10_000, [win]);
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  // Each client's countries over the day, for the row of places.
  const dest = usePoll(
    (s) => Promise.all(macs.map((m) => getJSON<Destinations>(`/api/devices/${enc(m)}/destinations?range=24h`, s).then((d) => [m, d.countries.map((c) => c.cc)] as const))),
    60_000,
    [macs.join(",")],
  ).data;
  const devs = macs.map((m) => (polled.data ?? []).find((d) => d.mac === m)).filter((d): d is Device => !!d);
  const rows = compareRows(devs, (g) => groupName(g, groups), windowLabel(win), Object.fromEntries(dest ?? []));
  return (
    <div class="page">
      <Header section="clients" win={win} onWin={onWin} />
      <main class="main">
        <Status polled={polled} />
        <ChipChain base={[{ label: "Clients", href: "#/clients" }]} last="Compare" />
        {!polled.data ? (
          <Loading />
        ) : devs.length < 2 ? (
          <Empty>Pick two or three clients on the Clients screen to compare them.</Empty>
        ) : (
          <section aria-label="Comparison" class="card card-flush">
            <div class="scroll-x">
              <table class="table compare-table">
                <thead>
                  <tr>
                    <th />
                    {devs.map((d) => (
                      <th key={d.mac}>
                        <ClientLink mac={d.mac} name={deviceName(d)} class="strong" />{" "}
                        <a class="muted small" href={compareHash(macs.filter((m) => m !== d.mac))} aria-label={`Leave ${deviceName(d)} out`}>×</a>
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  <tr>
                    <th class="muted small">Last hour</th>
                    {devs.map((d) => <td key={d.mac}><HourSparkline s={d.spark} /></td>)}
                  </tr>
                  {rows.map((r) => (
                    <tr key={r.label}>
                      <th class="muted small">{r.label}</th>
                      {r.cells.map((c, i) => (
                        <td key={devs[i].mac} class="small">
                          {c.list ? (
                            <span class="row-wrap">
                              {c.list.map((x) => (
                                <EntityLink key={x.text} e={x.e} title={countryName(x.text)}><CC cc={x.text} /></EntityLink>
                              ))}
                            </span>
                          ) : c.e ? (
                            <EntityLink e={c.e}>{c.text}</EntityLink>
                          ) : (
                            c.text
                          )}
                          {c.note && <div class="muted tiny">{c.note}</div>}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        )}
      </main>
    </div>
  );
}
