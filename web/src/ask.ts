// What the page says around the router's agent's answer: that a model wrote
// it, what it cost and what is left, and when a refused question can be
// asked again. No DOM here, so node tests cover it.

export interface AskReply { ok: boolean; answer?: string; costUsd?: number; asksLeft?: number; budgetLeft?: number; code?: string; message?: string; retryAfter?: number }

/** The line under an answer; what the agent did not say is left out, never shown as 0. */
export function askFooter(r: AskReply): string {
  const parts = ["Written by the model behind the router's agent"];
  if (r.costUsd !== undefined) parts.push(`cost $${r.costUsd.toFixed(4)}`);
  if (r.asksLeft !== undefined) parts.push(`${r.asksLeft} question${r.asksLeft === 1 ? "" : "s"} left this hour`);
  if (r.budgetLeft !== undefined) parts.push(`$${r.budgetLeft.toFixed(2)} left today`);
  return parts.join(" · ");
}

/** When a refused question can be asked again. */
export function askRetry(sec: number): string {
  if (!sec || sec <= 0) return "";
  return sec < 60 ? `Try again in ${sec} s.` : `Try again in ${Math.ceil(sec / 60)} min.`;
}
