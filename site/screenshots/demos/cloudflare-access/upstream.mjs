// Staging app for the cloudflare-access demo. It renders what the request
// told it about how it arrived — scheme and host from the proxy's forwarded
// headers, and whether Cloudflare Access vouched for the caller — so the
// camera can show that a tunnelled request reaches the app as https at the
// public hostname. It never echoes the Access token itself.
// Loopback only, fixed port so the .agnt.kdl proxy target is stable.
import http from 'node:http';

const PORT = 8031;

const page = (req) => {
  const proto = req.headers['x-forwarded-proto'] || 'http';
  const host = req.headers['x-forwarded-host'] || req.headers.host;
  const who = req.headers['cf-access-authenticated-user-email'];
  const viaAccess = !!req.headers['cf-access-jwt-assertion'];
  const row = (k, v, ok) => `<tr><th>${k}</th><td class="${ok ? 'ok' : ''}">${v}</td></tr>`;
  return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Acme — staging</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root { --bg:#0f1117; --panel:#171a23; --line:#262b38; --txt:#e6e9ef; --mut:#9aa3b2; --acc:#4f8cff; --ok:#4ade80; }
  * { margin:0; box-sizing:border-box; }
  body { font-family:-apple-system,'Segoe UI',Roboto,sans-serif; background:var(--bg); color:var(--txt); }
  main { max-width:760px; margin:0 auto; padding:48px 24px; }
  h1 { font-size:26px; margin-bottom:6px; }
  .sub { color:var(--mut); font-size:15px; margin-bottom:28px; }
  .panel { background:var(--panel); border:1px solid var(--line); border-radius:12px; padding:8px 20px; }
  table { width:100%; border-collapse:collapse; font-size:15px; }
  th, td { text-align:left; padding:12px 6px; border-bottom:1px solid var(--line); }
  tr:last-child th, tr:last-child td { border-bottom:none; }
  th { color:var(--mut); font-weight:600; width:42%; }
  td { font-family:ui-monospace,monospace; }
  td.ok { color:var(--ok); }
</style></head><body><main>
  <h1>Acme — staging build</h1>
  <p class="sub">Your dev server, as the request that reached it describes itself.</p>
  <div class="panel"><table>
    ${row('Reached at', `${proto}://${host}`, proto === 'https')}
    ${row('X-Forwarded-Proto', proto, proto === 'https')}
    ${row('Cloudflare Access', viaAccess ? 'verified at the edge and the origin' : 'not present (local)', viaAccess)}
    ${row('Access identity', who || (viaAccess ? 'service token' : '—'), viaAccess)}
  </table></div>
</main></body></html>`;
};

http.createServer((req, res) => {
  if (new URL(req.url, 'http://x').pathname !== '/') { res.writeHead(404); res.end('not found'); return; }
  res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
  res.end(page(req));
}).listen(PORT, '127.0.0.1', () => console.log(`access upstream on http://127.0.0.1:${PORT}/`));
