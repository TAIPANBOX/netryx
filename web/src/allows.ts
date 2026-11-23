// Allowance sections of the config that open nothing, for the Rules screen:
// named with why, so the owner never trusts one that does not work.

export interface BadAllow { id: string; member: string; why: string }

/** The warning lines: a heading, then each allowance, whose and why. */
export function invalidAllowsNote(list: BadAllow[], device: (mac: string) => string): string[] {
  if (list.length === 0) return [];
  const head = list.length === 1 ? "1 allowance in the config opens nothing:" : `${list.length} allowances in the config open nothing:`;
  return [head, ...list.map((b) => `${b.id}${b.member ? ` for ${device(b.member)}` : ""}: ${b.why}.`)];
}
