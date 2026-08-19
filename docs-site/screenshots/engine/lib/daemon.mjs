// Minimal agnt daemon client for demo scripting: just enough of the text
// protocol to start/stop the demo proxy and fire agent toasts without a live
// MCP session. One request per connection; replies end at ";;".
import net from 'node:net';

export const defaultSocketPath = () =>
  process.env.AGNT_SOCKET || `/tmp/devtool-mcp-${process.getuid()}/devtool-mcp.sock`;

// Protocol data framing (FormatCommand): VERB SUBVERB ARGS -- <b64len>\n<base64>;;
const frame = (line, data) => {
  let out = line;
  if (data !== undefined) {
    const b64 = Buffer.from(typeof data === 'string' ? data : JSON.stringify(data)).toString('base64');
    out += ` -- ${b64.length}\n${b64}`;
  }
  return out.endsWith(';;') ? out + '\n' : out + ';;\n';
};

const request = (line, socketPath = defaultSocketPath(), timeoutMs = 10000, data) => new Promise((resolve, reject) => {
  const conn = net.createConnection(socketPath);
  let buf = '';
  const timer = setTimeout(() => { conn.destroy(); reject(new Error('daemon request timed out')); }, timeoutMs);
  conn.on('connect', () => conn.write(frame(line, data)));
  conn.on('data', (d) => {
    buf += d.toString();
    if (buf.includes(';;')) {
      clearTimeout(timer);
      conn.end();
      resolve(buf.split(';;')[0].trim());
    }
  });
  conn.on('error', (e) => { clearTimeout(timer); reject(new Error(`daemon socket ${socketPath}: ${e.message}`)); });
});

export const ping = (socketPath) => request('PING;;', socketPath);

export const proxyStart = ({id, target, port}, socketPath) =>
  request(`PROXY START ${id} ${target} ${port};;`, socketPath);

export const proxyStop = (id, socketPath) => request(`PROXY STOP ${id};;`, socketPath);

// PROXY EXEC <id> with raw JS as data payload — runs in the content frame by
// default, same path as MCP proxy {action:"exec"/"navigate"}.
export const exec = async (proxyId, code, socketPath) => {
  const res = await request(`PROXY EXEC ${proxyId}`, socketPath, 15000, code);
  if (res.startsWith('ERR')) throw new Error(`PROXY EXEC ${proxyId}: ${res}`);
  return res;
};

// Walkthrough overlay control. There is NO dedicated walkthrough daemon verb:
// the MCP `walkthrough` tool (internal/tools/walkthrough_tools.go,
// buildWalkthroughExec → dt.client.ProxyExec) ships a JS snippet over
// PROXY EXEC, served by the hub's "EXEC" handler
// (internal/daemon/hub_proxy.go:30 → hubHandleProxyExec). The engine takes that
// same wire path through exec() above; buildWalkthroughCall mirrors the Go
// snippet builder so the browser-side entry point (window.__devtool.walkthrough)
// is identical. Nothing here invents a parallel path.
export const buildWalkthroughCall = (action, {script, scriptId, mode} = {}) => {
  const guard = (call) =>
    `(function(){var w=window.__devtool && window.__devtool.walkthrough; ` +
    `if(!w){return JSON.stringify({error:'walkthrough not available'});} ` +
    `return JSON.stringify(${call});})()`;

  switch (action) {
    case 'load':
      if (!script) throw new Error('walkthrough load: script required');
      return guard(`w.load(${JSON.stringify(script)})`);
    case 'start': {
      if (mode !== undefined && mode !== 'auto' && mode !== 'manual') {
        throw new Error(`walkthrough start: invalid mode ${JSON.stringify(mode)} (auto|manual)`);
      }
      const opts = `{mode:${JSON.stringify(mode || 'auto')}}`;
      if (script) return guard(`w.start(${JSON.stringify(script)}, ${opts})`);
      if (scriptId) return guard(`w.start(${JSON.stringify(scriptId)}, ${opts})`);
      throw new Error('walkthrough start: script or scriptId required');
    }
    case 'stop': case 'next': case 'prev':
    case 'play': case 'pause': case 'status': case 'list':
      return guard(`w.${action}()`);
    default:
      throw new Error(`walkthrough: unknown action ${JSON.stringify(action)}`);
  }
};

export const walkthrough = async (proxyId, action, opts, socketPath) => {
  const res = await exec(proxyId, buildWalkthroughCall(action, opts), socketPath);
  // The overlay reports its own failures inside the exec result; surface them
  // instead of letting a tour silently never appear in a take.
  if (/walkthrough not available|walkthrough host frame unavailable/.test(res)) {
    throw new Error(`walkthrough ${action} on ${proxyId}: ${res}`);
  }
  return res;
};

// CHAOS ADD-RULE <proxy-id> with JSON ChaosRuleConfig as data payload.
// Rule shape: {id, type: "latency"|"http_error"|..., enabled, url_pattern,
// methods, probability, min_latency_ms, max_latency_ms, error_codes, ...}
export const chaosAddRule = async (proxyId, rule, socketPath) => {
  const res = await request(`CHAOS ADD-RULE ${proxyId}`, socketPath, 10000, {chaos_rule: {enabled: true, ...rule}});
  if (res.startsWith('ERR')) throw new Error(`CHAOS ADD-RULE ${proxyId}: ${res}`);
  return res;
};

export const chaosClear = async (proxyId, socketPath) => {
  const res = await request(`CHAOS CLEAR ${proxyId};;`, socketPath);
  if (res.startsWith('ERR')) throw new Error(`CHAOS CLEAR ${proxyId}: ${res}`);
  return res;
};

// PROXY TOAST <proxy-id> with JSON ToastConfig as data payload.
// Throws on daemon ERR so a failed toast can't silently vanish from a take.
export const toast = async (proxyId, {message, type = 'info', title = '', duration = 4200}, socketPath) => {
  const res = await request(`PROXY TOAST ${proxyId}`, socketPath, 10000, {message, type, title, duration});
  if (res.startsWith('ERR')) throw new Error(`PROXY TOAST ${proxyId}: ${res}`);
  return res;
};
