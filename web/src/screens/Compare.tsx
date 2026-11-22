// Two or three clients side by side, from #/compare?mac=..&mac=..: the
// figures that tell them apart, and each one's last hour.

import { h } from "preact";
import type { Device, Group } from "../types";
import { getJSON, usePoll } from "../api";
import { Header, Status, Loading, Empty } from "../ui";
import { HourSparkline } from "../charts";
import { groupName } from "../format";
import { compareHash, compareRows } from "../compare";
import { ChipChain, ClientLink } from "./entity";
import { deviceName } from "./Clients";

export function CompareScreen({ macs }: { macs: string[] }) {
  const polled = usePoll((s) => getJSON<Device[]>("/api/devices", s), 10_000, []);
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 60_000, []).data ?? [];
  const devs = macs.map((m) => (polled.data ?? []).find((d) => d.mac === m)).filter((d): d is Device => !!d);
  const rows = compareRows(devs, (g) => groupName(g, groups));
  return (
    <div class="page">
      <Header section="clients" />
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
                      {r.values.map((v, i) => <td key={devs[i].mac} class="small">{v}</td>)}
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
