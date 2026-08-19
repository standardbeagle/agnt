// Minimal single-page app for the walkthrough-tour demo: three named regions
// the tour steps highlight in turn. Loopback only, fixed port so the proxy
// target is stable.
import http from 'node:http';

const PORT = 8027;

const page = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Acme — Releases</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root { --bg:#0f1117; --panel:#171a23; --line:#262b38; --txt:#e6e9ef; --mut:#9aa3b2; --acc:#4f8cff; }
  * { margin:0; box-sizing:border-box; }
  body { font-family:-apple-system,'Segoe UI',Roboto,sans-serif; background:var(--bg); color:var(--txt); }
  main { max-width:880px; margin:0 auto; padding:36px 24px; }
  h1 { font-size:24px; margin-bottom:6px; }
  .sub { color:var(--mut); font-size:14px; margin-bottom:28px; }
  .panel { background:var(--panel); border:1px solid var(--line); border-radius:12px; padding:22px; margin-bottom:20px; }
  .panel h2 { font-size:15px; margin-bottom:10px; }
  .panel p { color:var(--mut); font-size:14px; line-height:1.5; }
  .btn { display:inline-block; background:var(--acc); color:#fff; border:none; border-radius:8px;
         padding:11px 20px; font-size:14px; font-weight:600; cursor:pointer; }
</style></head><body><main>
  <h1 id="hero">Releases</h1>
  <p class="sub">Ship notes for everything that went out this week.</p>
  <div class="panel" id="changes">
    <h2>What changed</h2>
    <p>Twelve merges, two of them behind a flag. The proxy now keeps frame identity across in-frame navigations.</p>
  </div>
  <div class="panel" id="rollout">
    <h2>Rollout</h2>
    <p>Staged to 10% of dev machines. Promote when the error inbox stays quiet for a day.</p>
  </div>
  <button class="btn" id="promote">Promote release</button>
</main></body></html>`;

http.createServer((req, res) => {
  res.writeHead(200, {'Content-Type': 'text/html; charset=utf-8'});
  res.end(page);
}).listen(PORT, '127.0.0.1', () => console.log(`walkthrough-tour upstream on ${PORT}`));
