import { execFile, spawn, type ChildProcess } from "node:child_process";
import { createInterface } from "node:readline";
import { promisify } from "node:util";
import { tool, type Plugin } from "@opencode-ai/plugin";
import { compactionContext, meetsFloor, SEVERITIES, type Digest, type Severity } from "./queue.ts";

const run = promisify(execFile);

interface Options {
  /** Lowest severity shown as a toast. Default "warning". */
  severity?: Severity;
  /** `agnt` binary. Default $AGNT_BIN, else "agnt". */
  bin?: string;
}

/**
 * Shows agnt incident pings in opencode and exposes the inbox as tools.
 *
 * It never injects text into a session: start opencode with `agnt run opencode`
 * and agnt types each ping to the agent (the stdin connector); this plugin is the
 * display, the pull tools and the compaction context. It needs the agent's own
 * session (AGNT_SESSION_CODE) because inboxes are per session.
 */
export const AgntPlugin: Plugin = async (ctx, raw?: Options) => {
  const { client, directory } = ctx;
  const opts = raw ?? {};
  const bin = opts.bin ?? process.env.AGNT_BIN ?? "agnt";
  const floor = opts.severity ?? "warning";
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
        if (d.type !== "incident_digest" || !meetsFloor(d.level, floor)) return;
        void client.tui
          .showToast({ body: { message: d.message, variant: d.level === "critical" || d.level === "error" ? "error" : "warning" } })
          .catch(() => undefined);
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
