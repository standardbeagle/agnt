// Pure helpers for the opencode-agnt plugin. No opencode or node imports, so they
// are unit-testable on their own. The plugin only displays and pulls: agnt sends
// each agent message down the one connector the session was started with, so the
// plugin never injects text into a session itself.

export const SEVERITIES = ["info", "warning", "error", "critical"] as const;
export type Severity = (typeof SEVERITIES)[number];

export interface PingSummary {
  critical?: number;
  error?: number;
  warning?: number;
  info?: number;
  new?: number;
}

/** One line of `agnt incidents watch`. */
export interface Digest {
  type: "incident_digest";
  session_code: string;
  level: string;
  message: string;
  payload?: { summary?: PingSummary; cursor?: string };
}

const rank = (s: string): number => {
  const i = SEVERITIES.indexOf(s as Severity);
  return i < 0 ? 0 : i;
};

/** True when `level` is at or above `floor`. An unknown level counts as info. */
export const meetsFloor = (level: string, floor: Severity): boolean => rank(level) >= rank(floor);

/** Text carried through compaction so the new context keeps the incident state. */
export function compactionContext(
  cursor: string | undefined,
  incidents: { severity: string; summary?: string; count?: number; read?: boolean }[],
  max = 5,
): string | undefined {
  const open = incidents.filter((i) => !i.read);
  if (open.length === 0) return undefined;
  const lines = open
    .slice(0, max)
    .map((i) => `- [${i.severity}] ${i.summary ?? "(no summary)"}${i.count && i.count > 1 ? ` x${i.count}` : ""}`);
  const more = open.length > max ? `\n(+${open.length - max} more)` : "";
  return (
    `agnt incidents still unread: ${open.length}` +
    (cursor ? ` (cursor ${cursor})` : "") +
    `\n${lines.join("\n")}${more}\nUse the agnt_incidents tool for the full list.`
  );
}
