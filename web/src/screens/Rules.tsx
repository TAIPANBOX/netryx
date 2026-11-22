// Rules: every rule with what it blocks, for whom and when, and whether it
// works now; a rule is added or changed in three steps (what, who, when). The
// router is the only judge of a rule: what it refuses is shown beside the form.

import { EntityLink } from "./entity";
import { h, Fragment } from "preact";
import { useContext, useEffect, useRef, useState } from "preact/hooks";
import type { Device, Overview, Group } from "../types";
import { getJSON, sendJSON, usePoll } from "../api";
import { Header, Status, Loading, Empty, Pills, useSession, IconContext } from "../ui";
import { time, BLOCKABLE_CATEGORIES, category } from "../format";
import {
  DAYS, describeRule, formFromRule, formProblem, fromAgent, appChoices, isPause, manifestName, ruleFromForm, stateOf, liveAllows, allowText, ruleVerb,
  type AppChoice, type Names, type Rule, type RuleForm,
} from "../rules";
import { CDN_NOTE } from "./controls";
import { deviceName } from "./Clients";


const EMPTY: RuleForm = { what: "app", targets: "", who: "all", members: [], group: "", days: [], from: "", to: "" };

export function Rules({ focus = "" }: { focus?: string }) {
  const { canWrite } = useSession();
  const [n, setN] = useState(0);
  const rules = usePoll((s) => getJSON<Rule[]>("/api/rules", s), 10_000, [n]);
  const devices = usePoll((s) => getJSON<Device[]>("/api/devices", s), 30_000, []);
  const groups = usePoll((s) => getJSON<Group[]>("/api/groups", s), 0, [n]);
  const manifest = useContext(IconContext);
  // Apps the console has seen, so the picker works even without icon names.
  const seen = usePoll((s) => getJSON<Overview>("/api/overview?range=30d&split=app", s), 0, []);
  const [editing, setEditing] = useState<string>(""); // a rule id, "new", or ""
  const refresh = () => setN((x) => x + 1);

  // App names come from the icon manifest the page already loads.
  const appList: AppChoice[] = appChoices(manifest, (seen.data?.topApps ?? []).filter((a) => !a.other));
  const names: Names = {
    app: manifestName(manifest),
    device: (m) => {
      const d = (devices.data ?? []).find((x) => x.mac.toLowerCase() === m.toLowerCase());
      return d ? deviceName(d) : m.toUpperCase();
    },
    group: (id) => (groups.data ?? []).find((g) => g.id === id)?.name ?? id,
  };
  const list = rules.data;
  const now = Date.now();

  return (
    <div class="page">
      <Header section="rules" />
      <main class="main">
        <div class="toolbar">
          <h1>Rules</h1>
          <span class="muted small">what is blocked, for whom and when; a pause is a rule too</span>
          <a class="btn btn-ghost push" href="#/rules/filters">DNS filters</a>
          {canWrite && editing !== "new" && (
            <button type="button" class="btn" onClick={() => setEditing("new")}>New rule</button>
          )}
        </div>
        <Status polled={rules} />
        {editing === "new" && (
          <RuleEditor
            title="New rule"
            start={EMPTY}
            devices={devices.data ?? []}
            groups={groups.data ?? []}
            apps={appList}
            save={(b) => sendJSON("POST", "/api/rules", b)}
            done={() => { setEditing(""); refresh(); }}
          />
        )}
        {!list ? (
          rules.error ? null : <Loading />
        ) : list.length === 0 ? (
          editing === "new" ? null : <Empty>No rules yet. A country or an app is blocked with the button beside it, or here: New rule.</Empty>
        ) : (
          <div class="stack">
            {list.map((r) => (
              <Fragment key={r.id}>
                {editing === r.id ? (
                  <RuleEditor
                    title="Edit rule"
                    start={formFromRule(r)}
                    devices={devices.data ?? []}
                    groups={groups.data ?? []}
                    apps={appList}
                    save={(b) => sendJSON("PUT", `/api/rules/${encodeURIComponent(r.id)}`, b)}
                    done={() => { setEditing(""); refresh(); }}
                    cancel={() => setEditing("")}
                  />
                ) : (
                  <RuleCard r={r} names={names} now={now} canWrite={canWrite} onEdit={() => setEditing(r.id)} refresh={refresh} focused={r.id === focus} />
                )}
              </Fragment>
            ))}
          </div>
        )}
      </main>
    </div>
  );
}

function RuleCard({ r, names, now, canWrite, onEdit, refresh, focused }: { r: Rule; names: Names; now: number; canWrite: boolean; onEdit: () => void; refresh: () => void; focused?: boolean }) {
  const box = useRef<HTMLElement>(null);
  // A link to one rule brings it into view and marks it.
  useEffect(() => {
    if (focused) box.current?.scrollIntoView({ block: "center" });
  }, [focused]);
  const st = stateOf(r, now);
  const [err, setErr] = useState("");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [ask, setAsk] = useState(false);
  const act = async (fn: () => Promise<string | void>) => {
    setBusy(true);
    setErr("");
    setNote("");
    try {
      const out = await fn();
      if (out) setNote(out);
      refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  // "30 min more" names a device, so it is offered where there is exactly one,
  // never for a country (the router refuses a time allowance on one).
  const one = r.who === "device" && r.members.length === 1 ? r.members[0] : "";
  const canMore = canWrite && !!one && r.what !== "country" && st.kind === "active";
  const toggle = () =>
    act(async () => {
      await sendJSON("PUT", `/api/rules/${encodeURIComponent(r.id)}`, ruleFromForm({ ...formFromRule(r), enabled: !r.enabled }));
    });

  return (
    <section ref={box} class={`card rule${st.kind === "active" ? " rule-active" : ""}${focused ? " rule-focused" : ""}`} aria-label={`Rule ${r.id}`}>
      <div class="rule-head">
        <span class="strong break">{describeRule(r, names)}</span>
        <span class={`state state-${st.kind}`}>
          <span class="dot" aria-hidden="true" />
          {st.text}
        </span>
      </div>
      <RuleLinks r={r} names={names} />
      <div class="row-wrap small soft">
        {fromAgent(r) && <span class="tag">by the agent</span>}
        {isPause(r) && <span class="tag">pause</span>}
        <span>Hits since the rules started: {r.hits}</span>
      </div>
      {liveAllows(r, now).map((a) => (
        <p key={a.id} class="small soft rule-note">{names.device ? names.device(a.member) : a.member}: {allowText(a)}</p>
      ))}
      {r.what === "country" && <p class="small muted rule-note">The country is told by the address: {CDN_NOTE}.</p>}
      {canWrite && (
        <div class="actions">
          {canMore && (
            <button
              type="button"
              class="btn"
              disabled={busy}
              onClick={() =>
                act(async () => {
                  const a = await sendJSON<{ until: number }>("POST", "/api/allow", { rule: r.id, mac: one, minutes: 30 });
                  return `Allowed 30 min more, until ${time(new Date(a.until * 1000).toISOString())}.`;
                })
              }
            >
              30 min more
            </button>
          )}
          <button type="button" class="btn" disabled={busy || !!r.error} title={r.error ? "The rule cannot be read: edit it first" : undefined} onClick={toggle}>{r.enabled ? "Turn off" : "Turn on"}</button>
          <button type="button" class="btn" disabled={busy} onClick={onEdit}>Edit</button>
          {!ask ? (
            <button type="button" class="btn btn-ghost" disabled={busy} onClick={() => setAsk(true)}>Delete</button>
          ) : (
            <span class="row-wrap" role="group" aria-label="Delete rule">
              <span class="small soft">Delete the rule?</span>
              <button type="button" class="btn btn-danger" disabled={busy} onClick={() => act(async () => { await sendJSON("DELETE", `/api/rules/${encodeURIComponent(r.id)}`); })}>Yes, delete</button>
              <button type="button" class="btn btn-ghost" onClick={() => setAsk(false)}>No</button>
            </span>
          )}
        </div>
      )}
      {note && <span class="act-note" role="status">{note}</span>}
      {err && <span class="act-err" role="alert">{err}</span>}
    </section>
  );
}

function RuleEditor(p: {
  title: string; start: RuleForm; devices: Device[]; groups: Group[]; apps: AppChoice[];
  save: (body: ReturnType<typeof ruleFromForm>) => Promise<unknown>; done: () => void; cancel?: () => void;
}) {
  const [f, setF] = useState<RuleForm>(p.start);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const set = (o: Partial<RuleForm>) => setF({ ...f, ...o });
  const toggleIn = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

  const submit = async (e: Event) => {
    e.preventDefault();
    const bad = formProblem(f);
    if (bad) {
      setErr(bad);
      return;
    }
    setBusy(true);
    setErr("");
    try {
      await p.save(ruleFromForm(f));
      p.done();
    } catch (x) {
      setErr(x instanceof Error ? x.message : String(x));
    } finally {
      setBusy(false);
    }
  };
  const hint =
    f.what === "country" ? "Countries as two letters, comma separated, for example RU, CN." :
    f.what === "app" ? "Apps by the name the hint gives, for example tiktok, youtube." :
    f.what === "category" ? "Categories from the hint, for example video, social." : "";
  const list = f.what === "app" ? "apps-list" : f.what === "category" ? "cats-list" : undefined;

  return (
    <form class="card editor" onSubmit={submit} aria-label={p.title}>
      <h2>{p.title}</h2>
      <fieldset class="step">
        <legend>1. What to block</legend>
        <Pills
          label="What to block"
          inset
          value={f.what}
          onPick={(v) => set({ what: v as RuleForm["what"] })}
          items={[{ id: "app", label: "Apps" }, { id: "country", label: "Countries" }, { id: "category", label: "Categories" }, { id: "all", label: "The whole internet" }]}
        />
        {f.what !== "all" && (
          <label class="field">
            <span class="small muted">{hint}</span>
            <input class="input" list={list} value={f.targets} onInput={(e) => set({ targets: (e.target as HTMLInputElement).value })} placeholder={f.what === "country" ? "RU, CN" : ""} autocapitalize="none" spellcheck={false} />
          </label>
        )}
        {f.what === "country" && <span class="small muted">The country is told by the address: {CDN_NOTE}.</span>}
        <datalist id="apps-list">{p.apps.map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</datalist>
        <datalist id="cats-list">{BLOCKABLE_CATEGORIES.map((c) => <option key={c} value={c}>{category(c)}</option>)}</datalist>
      </fieldset>
      <fieldset class="step">
        <legend>2. Who</legend>
        <Pills
          label="Who"
          inset
          value={f.who}
          onPick={(v) => set({ who: v as RuleForm["who"] })}
          items={[{ id: "all", label: "Everyone" }, { id: "device", label: "Devices" }, { id: "group", label: "A group" }]}
        />
        {f.who === "device" && (
          <div class="checks">
            {p.devices.map((d) => (
              <label key={d.mac} class="check">
                <input type="checkbox" checked={f.members.includes(d.mac.toLowerCase())} onChange={() => set({ members: toggleIn(f.members, d.mac.toLowerCase()) })} />
                <span>{deviceName(d)}</span>
              </label>
            ))}
            {p.devices.length === 0 && <span class="small muted">No devices yet.</span>}
          </div>
        )}
        {f.who === "group" && (
          p.groups.length === 0 ? (
            <span class="small muted">No groups yet.</span>
          ) : (
            <select class="input" value={f.group} aria-label="Group" onChange={(e) => set({ group: (e.target as HTMLSelectElement).value })}>
              <option value="">Pick a group</option>
              {p.groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
            </select>
          )
        )}
      </fieldset>
      <fieldset class="step">
        <legend>3. When</legend>
        {(f.what === "app" || f.what === "category") && (
          <Pills
            label="Block or limit"
            inset
            value={f.action ?? "block"}
            onPick={(v) => set({ action: v as RuleForm["action"], minutes: f.minutes || "60" })}
            items={[{ id: "block", label: "Block" }, { id: "limit", label: "Time limit" }]}
          />
        )}
        {f.action === "limit" && (
          <label class="field">
            <span class="small muted">Minutes a day (a minute counts when the app moved more than 64 KB)</span>
            <input class="input" type="number" min="1" max="1440" value={f.minutes ?? ""} onInput={(e) => set({ minutes: (e.target as HTMLInputElement).value })} />
          </label>
        )}
        <div class="row-wrap" role="group" aria-label="Days">
          {DAYS.map((d) => (
            <button key={d.id} type="button" class={f.days.includes(d.id) ? "chip on" : "chip"} aria-pressed={f.days.includes(d.id)} onClick={() => set({ days: toggleIn(f.days, d.id) })}>{d.short}</button>
          ))}
        </div>
        {f.action !== "limit" && <div class="row-wrap">
          <label class="field"><span class="small muted">From</span><input class="input" type="time" value={f.from} onInput={(e) => set({ from: (e.target as HTMLInputElement).value })} /></label>
          <label class="field"><span class="small muted">To</span><input class="input" type="time" value={f.to} onInput={(e) => set({ to: (e.target as HTMLInputElement).value })} /></label>
        </div>}
        <span class="small muted">{f.action === "limit"
          ? "A time limit counts per day and starts again at midnight on the router's clock; days, if picked, limit when it applies."
          : "Without days and hours the rule always applies. A span across midnight (21:00-07:00) works too."}</span>
      </fieldset>
      {err && <p class="banner" role="alert">{err}</p>}
      <div class="actions">
        <button type="submit" class="btn btn-primary" disabled={busy}>Save</button>
        <button type="button" class="btn btn-ghost" onClick={() => (p.cancel ? p.cancel() : p.done())}>Cancel</button>
      </div>
    </form>
  );
}

/** What a rule names, each a link: its targets, and whom it reaches. */
function RuleLinks({ r, names }: { r: Rule; names: Names }) {
  const targets = r.targets.map((t) =>
    r.what === "country" ? <EntityLink key={t} e={{ kind: "country", cc: t }}>{t}</EntityLink>
    : r.what === "app" ? <EntityLink key={t} e={{ kind: "app", id: t }}>{names.app ? names.app(t) : t}</EntityLink>
    : r.what === "category" ? <EntityLink key={t} e={{ kind: "category", id: t }}>{t}</EntityLink>
    : <span key={t}>{t}</span>,
  );
  const who =
    r.who === "group" ? <EntityLink e={{ kind: "group", id: r.group }}>{names.group ? names.group(r.group) : r.group}</EntityLink>
    : r.who === "device" ? r.members.map((m) => <EntityLink key={m} e={{ kind: "client", mac: m }}>{names.device ? names.device(m) : m}</EntityLink>)
    : <span>all devices</span>;
  return (
    <div class="row-wrap small rule-links">
      <span class="muted">{ruleVerb(r)}</span>
      {targets}
      <span class="muted">for</span>
      {who}
    </div>
  );
}
