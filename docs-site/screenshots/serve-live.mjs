// Upstream server for the live-stack demo recording (record-live.mjs).
//
// Serves page.html RAW — without the embedded bundle — because in this flow
// the real agnt proxy sits in front and injects the __devtool bundle itself,
// exactly as it does for a real dev server. Mirrors the mock /api endpoints
// from capture.mjs so the audits and the dynamic-form failure cases have
// real traffic. Loopback only, fixed port so the proxy target is stable.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const PORT = 8021;

const pageHTML = fs.readFileSync(path.join(here, 'page.html'), 'utf8');

function mockAPI(req, res) {
  const u = new URL(req.url, 'http://x');
  const respond = (delay, body, status = 200) => setTimeout(() => {
    res.writeHead(status, {'Content-Type': 'application/json'});
    res.end(JSON.stringify(body));
  }, delay);
  if (req.method === 'POST' && u.pathname === '/api/customer') {
    let raw = '';
    req.on('data', (c) => { raw += c; });
    req.on('end', () => {
      let b = {};
      try { b = JSON.parse(raw); } catch { return respond(80, {error: 'malformed JSON body'}, 400); }
      if (b.company === 'Globex Corp') return respond(200, {error: 'company already exists', field: 'company'}, 409);
      if (b.plan === 'Enterprise' && b.seats < 25) return respond(150, {error: 'Enterprise plan requires at least 25 seats', field: 'seats'}, 422);
      if (b.company === 'Crash Test') return respond(300, {error: 'internal server error'}, 500);
      return respond(180, {id: 1042, company: b.company});
    });
    return;
  }
  if (u.pathname === '/api/users') return respond(260, {users: [1, 2, 3, 4, 5].map(i => ({id: i}))});
  if (u.pathname.startsWith('/api/user/')) return respond(140, {id: u.pathname.split('/').pop(), name: 'user'});
  if (u.pathname === '/api/config') return respond(90, {theme: 'dark', flags: {beta: true}});
  if (u.pathname === '/api/report') return respond(420, {ok: true});
  res.writeHead(404); res.end('{}');
}

const server = http.createServer((req, res) => {
  if (req.url.startsWith('/api/')) return mockAPI(req, res);
  res.writeHead(200, {'Content-Type': 'text/html'});
  res.end(pageHTML);
});
server.listen(PORT, '127.0.0.1', () => console.log(`live upstream on http://127.0.0.1:${PORT}/`));
