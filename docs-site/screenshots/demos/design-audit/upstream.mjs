// Per-demo upstream for design-audit: serves slop.html on 8022 so the shared
// serve-live.mjs (8021, dashboard page) stays untouched. Loopback only.
import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const html = fs.readFileSync(path.join(here, 'slop.html'), 'utf8');
const PORT = 8022;

http.createServer((req, res) => {
  res.writeHead(200, {'Content-Type': 'text/html'});
  res.end(html);
}).listen(PORT, '127.0.0.1', () => console.log(`slop upstream on http://127.0.0.1:${PORT}/`));
