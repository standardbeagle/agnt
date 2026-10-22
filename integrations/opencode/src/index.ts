import { execFile, spawn, type ChildProcess } from "node:child_process";
import { createInterface } from "node:readline";
import { promisify } from "node:util";
import { tool, type Plugin } from "@opencode-ai/plugin";
import {
  compactionContext,
  decide,
  DigestQueue,
  SEVERITIES,
  type Digest,
  type Severity,
} from "./queue.ts";

const run = promisify(execFile);

interface Options {
  /** Lowest severity pushed into the session. Default "warning". */
  severity?: Severity;
  /** Coalescing window for a burst of digests, ms. Default 2000. */
  coalesceMs?: number;
  /** Identical batches inside this window are dropped, ms. Default 30000. */
  dedupMs?: number;
  /** When idle, start a turn on a new batch instead of leaving it as context. Default true. */
  resumeOnIdle?: boolean;
  /** `agnt` binary. Default $AGNT_BIN, else "agnt". */
  bin?: string;
}

/**
 * Bridges the agnt incident pipeline into opencode.
 *
 * Needs the agent's own agnt session: start opencode with `agnt run opencode`,
 * which sets AGNT_SESSION_CODE. Inboxes are per session, so the plugin only ever
 * sees this agent's incidents. The daemon applies forwarding-pause and
 * alerts.push before a digest reaches `agnt incidents watch`.
 */
export const AgntPlugin: Plugin = async (ctx, raw?: Options) => {
  const { client, directory } = ctx;
  const opts = raw ?? {};
  const bin = opts.bin ?? process.env.AGNT_BIN ?? "agnt";
  const session = process.env.AGNT_SESSION_CODE;
  const env = { ...process.env, AGNT_SESSION_CODE: session ?? "" };

  const log = (level: "debug" | "info" | "warn" | "error", message: string, extra?: Record<string, unknown>) =>
    client.app.log({ body: { service: "opencode-agnt", level, message, extra } }).catch(() => undefined);

  if (!session) {
    // Declared integration must not fail silently (agnt: no silent failures).
    await log("warn", "AGNT_SESSION_CODE is not set; start opencode with `agnt run opencode`. Plugin inactive.");
    return {};
  }

  const agnt = async (...args: string[]): Promise<string> => {
    try {
      const { stdout } = await run(bin, args, { cwd: directory, env, timeout: 15_000, windowsHide: true });
      return stdout.trim();
    } catch (e) {
      const err = e as { stderr?: string; message: string };
      throw new Error((err.stderr || err.message).trim());
    }
  };

  // Which opencode session receives digests: the most recently active root
  // session. Subagent sessions (parentID set) are never targets.
  const sessions = new Map<string, { busy: boolean; at: number }>();
  const subagents = new Set<string>();
  const touch = (id: string | undefined, busy?: boolean) => {
    if (!id || subagents.has(id)) return;
    const cur = sessions.get(id);
    sessions.set(id, { busy: busy ?? cur?.busy ?? false, at: Date.now() });
  };
  const target = () => {
    let best: [string, { busy: boolean; at: number }] | undefined;
    for (const e of sessions) if (!best || e[1].at > best[1].at) best = e;
    return best;
  };

  const queue = new DigestQueue(opts.severity ?? "warning", opts.dedupMs ?? 30_000);
  let timer: ReturnType<typeof setTimeout> | undefined;

  const send = async (id: string, text: string, mode: "context" | "prompt") => {
    const body = {
      noReply: mode === "context",
      // synthetic keeps the text out of the human-visible transcript (opencode
      // part schema); servers that do not know the field ignore it.
      parts: [{ type: "text", text, synthetic: true }],
    };
    const s = client.session as unknown as {
      promptAsync?: (a: unknown) => Promise<unknown>;
      prompt: (a: unknown) => Promise<unknown>;
    };
    // promptAsync returns at once; plain prompt waits for the reply, so a
    // turn-starting send must not be awaited by this loop.
    if (mode === "prompt" && typeof s.promptAsync === "function") {
      await s.promptAsync({ path: { id }, body });
    } else if (mode === "prompt") {
      void s.prompt({ path: { id }, body }).catch((e) => log("error", `prompt failed: ${e}`));
    } else {
      await s.prompt({ path: { id }, body });
    }
  };

  const flush = async () => {
    timer = undefined;
    const t = target();
    // No session yet: leave the batch pending for the first one (session events
    // reschedule) rather than taking it and losing it to the dedup window.
    if (!t) return;
    const batch = queue.take();
    if (!batch) return;
    const mode = decide({ hasBatch: true, busy: t[1].busy, resumeOnIdle: opts.resumeOnIdle ?? true });
    if (mode === "none") return;
    try {
      await send(t[0], batch.text, mode);
      if (mode === "prompt") touch(t[0], true);
      // A toast is a human cue only; the session injection is the delivery.
      await client.tui.showToast({ body: { message: `agnt: ${batch.count} incident ping(s), highest ${batch.level}`, variant: batch.level === "critical" || batch.level === "error" ? "error" : "warning" } }).catch(() => undefined);
    } catch (e) {
      await log("error", `incident delivery failed: ${e}`);
    }
  };

  const schedule = () => {
    if (!timer) timer = setTimeout(() => void flush(), opts.coalesceMs ?? 2000);
  };

  // `agnt incidents watch` is a long-lived NDJSON child that reconnects through
  // daemon restarts itself; this loop only restarts the child if it dies.
  let child: ChildProcess | undefined;
  let backoff = 1000;
  const watch = () => {
    child = spawn(bin, ["incidents", "watch"], { cwd: directory, env, windowsHide: true, stdio: ["ignore", "pipe", "pipe"] });
    createInterface({ input: child.stdout! }).on("line", (line) => {
      backoff = 1000;
      try {
        const d = JSON.parse(line) as Digest;
        if (d.type === "incident_digest" && queue.add(d)) schedule();
      } catch {
        void log("warn", `unparseable watch line: ${line.slice(0, 120)}`);
      }
    });
    child.stderr!.on("data", (b) => void log("debug", String(b).trim()));
    child.on("error", (e) => void log("error", `cannot run ${bin}: ${e.message}`));
    child.on("exit", (code) => {
      void log("warn", `agnt incidents watch exited (${code}); restarting in ${backoff}ms`);
      setTimeout(watch, backoff);
      backoff = Math.min(backoff * 2, 30_000);
    });
  };
  watch();
  process.on("exit", () => child?.kill());

  return {
    event: async ({ event }) => {
      const p = (event as { properties?: Record<string, any> }).properties ?? {};
      switch (event.type) {
        case "session.created":
          if (p.info?.parentID) subagents.add(p.info.id);
          else touch(p.info?.id, false);
          break;
        case "session.status":
          touch(p.sessionID, p.status?.type === "busy" || p.status?.type === "retry");
          if (queue.hasPending()) schedule();
          break;
        case "session.idle":
          touch(p.sessionID, false);
          // A batch that waited out a busy turn goes out now.
          if (queue.hasPending()) schedule();
          break;
        case "session.deleted":
          sessions.delete(p.info?.id);
          break;
      }
    },

    // Keep the incident cursor and unresolved findings across compaction, so the
    // new context still knows what is outstanding and how to page past it.
    "experimental.session.compacting": async (_input, output) => {
      try {
        const res = JSON.parse(await agnt("incidents", "list", "--json", "--limit", "20"));
        const text = compactionContext(res.cursor, res.incidents ?? []);
        if (text) output.context.push(text);
      } catch (e) {
        await log("warn", `compaction context skipped: ${e}`);
      }
    },

    tool: {
      agnt_incidents: tool({
        description:
          "List this session's agnt incidents (browser errors, process crashes, failed requests) without marking them read. " +
          "Pass `since` (the cursor from a previous result) to page.",
        args: {
          severity: tool.schema.string().optional().describe(`Comma-separated: ${SEVERITIES.join(",")}`),
          since: tool.schema.string().optional().describe("Cursor from a previous result"),
          limit: tool.schema.number().optional().describe("Max incidents, default 20"),
        },
        async execute(args) {
          const a = ["incidents", "list", "--json"];
          if (args.severity) a.push("--severity", args.severity);
          if (args.since) a.push("--since", args.since);
          if (args.limit) a.push("--limit", String(args.limit));
          return agnt(...a);
        },
      }),
      agnt_forwarding: tool({
        description:
          "Pause or resume agnt's push of incident pings into this session. Incidents still collect and stay readable with agnt_incidents.",
        args: { action: tool.schema.string().describe("pause or resume") },
        async execute(args) {
          if (args.action !== "pause" && args.action !== "resume") return "action must be pause or resume";
          return agnt("incidents", "forwarding", args.action);
        },
      }),
    },
  };
};

export default AgntPlugin;
