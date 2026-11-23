// "Ask the router's agent": for a session that may write, beside the chart's
// spike button once a stretch is picked. The router sends the agent the
// question and its own summary of the stretch; the answer opens in a card that
// says a model wrote it. The agent's key never reaches the page.

import { h } from "preact";
import { useContext, useState } from "preact/hooks";
import { getJSON, sendJSON, usePoll } from "../api";
import { SpanContext, useSession } from "../ui";
import { askFooter, askRetry, type AskReply } from "../ask";
import { useCards } from "./windows";
import type { Span } from "../span";

export function AskButton() {
  const s = useSession();
  const span = useContext(SpanContext);
  const { open } = useCards();
  const st = usePoll((sig) => getJSON<{ available: boolean; why: string }>("/api/agent", sig), 60_000, []).data;
  if (!s.canWrite || !span || !st) return null;
  if (!st.available) return <span class="muted small" title={st.why}>Router's agent: not enabled on this router</span>;
  return (
    <button type="button" class="link-btn small" onClick={(e) => open("ask", { title: "Router's agent", body: () => <AskCard span={span} /> }, e.currentTarget as Element)}>
      Ask the router's agent
    </button>
  );
}

function AskCard({ span }: { span: Span }) {
  const [q, setQ] = useState("What made the traffic in this stretch? Answer in plain words.");
  const [busy, setBusy] = useState(false);
  const [r, setR] = useState<AskReply | null>(null);
  const ask = async () => {
    setBusy(true);
    setR(null);
    try {
      setR(await sendJSON<AskReply>("POST", "/api/agent/ask", { question: q, from: span.from, to: span.to }));
    } catch (e) {
      setR({ ok: false, message: e instanceof Error ? e.message : String(e) });
    }
    setBusy(false);
  };
  return (
    <div class="col-tight ask-card">
      <textarea class="ask-q" rows={3} maxLength={2000} value={q} onInput={(e) => setQ((e.target as HTMLTextAreaElement).value)} aria-label="Question" />
      <button type="button" class="btn self-start" disabled={busy || !q.trim()} onClick={ask}>{busy ? "Asking…" : "Ask"}</button>
      {busy && <span class="muted small">The agent is reading the router; this can take up to two minutes.</span>}
      {r && r.ok && <p class="ask-answer">{r.answer}</p>}
      {r && r.ok && <span class="muted small">{askFooter(r)}</span>}
      {r && !r.ok && <p class="bad-text small">{r.message} {askRetry(r.retryAfter ?? 0)}</p>}
    </div>
  );
}
