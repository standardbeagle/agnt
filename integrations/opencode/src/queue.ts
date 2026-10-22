// Pure delivery logic for the opencode-agnt plugin. No opencode or node imports,
// so it is unit-testable on its own. It ports the gates of agnt's single-queue
// rule (.claude/skills/messaging-queue): severity floor, dedup, coalescing and
// activity-defer. The daemon already applies forwarding-pause and alerts.push
// before a digest reaches the plugin.

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

export interface Batch {
  level: Severity;
  /** Digests folded into this batch. */
  count: number;
  summary: PingSummary;
  text: string;
}

const rank = (s: string): number => {
  const i = SEVERITIES.indexOf(s as Severity);
  return i < 0 ? 0 : i;
};

/** True when `level` is at or above `floor`. An unknown level counts as info. */
export const meetsFloor = (level: string, floor: Severity): boolean => rank(level) >= rank(floor);

/**
 * The text injected into the session. It names the plugin's own pull tool: the
 * daemon's one-line digest says "pull: get_incidents", an MCP tool this plugin's
 * host may not have.
 */
export function batchText(level: Severity, summary: PingSummary): string {
  const s = summary;
  return (
    `[agnt] incidents: crit=${s.critical ?? 0} err=${s.error ?? 0} warn=${s.warning ?? 0} ` +
    `(new=${s.new ?? 0}), highest ${level}. Inspect with the agnt_incidents tool.`
  );
}

export class DigestQueue {
  private pending: { level: Severity; count: number; summary: PingSummary } | undefined;
  private lastText = "";
  private lastAt = -Infinity;

  private readonly floor: Severity;
  private readonly dedupMs: number;
  private readonly now: () => number;

  // Plain fields, not parameter properties: node's type stripping rejects those.
  constructor(floor: Severity, dedupMs: number, now: () => number = Date.now) {
    this.floor = floor;
    this.dedupMs = dedupMs;
    this.now = now;
  }

  /** Folds a digest into the pending batch. Returns false when it was dropped. */
  add(d: Digest): boolean {
    if (!meetsFloor(d.level, this.floor)) return false;
    const level = SEVERITIES[rank(d.level)];
    const summary = d.payload?.summary ?? {};
    if (!this.pending) {
      this.pending = { level, count: 1, summary };
    } else {
      // Each ping carries the inbox's current totals, so the newest summary
      // replaces the old; only the severity ceiling is sticky.
      this.pending = {
        level: rank(level) > rank(this.pending.level) ? level : this.pending.level,
        count: this.pending.count + 1,
        summary,
      };
    }
    return true;
  }

  hasPending(): boolean {
    return this.pending !== undefined;
  }

  /**
   * Takes the batch for delivery, or undefined when none is pending or it
   * repeats what was just delivered (same text inside the dedup window).
   */
  take(): Batch | undefined {
    const p = this.pending;
    this.pending = undefined;
    if (!p) return undefined;
    const text = batchText(p.level, p.summary);
    const t = this.now();
    if (text === this.lastText && t - this.lastAt < this.dedupMs) return undefined;
    this.lastText = text;
    this.lastAt = t;
    return { ...p, text };
  }
}

export type Delivery = "none" | "context" | "prompt";

/**
 * Activity-defer. While the agent is working a batch rides in as context only
 * (no reply): the running loop sees it on its next step with no extra model
 * round trip. When idle, `resumeOnIdle` decides whether the batch starts a turn
 * or waits as context for the next one.
 */
export function decide(opts: { hasBatch: boolean; busy: boolean; resumeOnIdle: boolean }): Delivery {
  if (!opts.hasBatch) return "none";
  if (opts.busy) return "context";
  return opts.resumeOnIdle ? "prompt" : "context";
}

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
