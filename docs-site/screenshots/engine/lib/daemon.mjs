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

// PROXY TOAST <proxy-id> with JSON ToastConfig as data payload.
// Throws on daemon ERR so a failed toast can't silently vanish from a take.
export const toast = async (proxyId, {message, type = 'info', title = '', duration = 4200}, socketPath) => {
  const res = await request(`PROXY TOAST ${proxyId}`, socketPath, 10000, {message, type, title, duration});
  if (res.startsWith('ERR')) throw new Error(`PROXY TOAST ${proxyId}: ${res}`);
  return res;
};
