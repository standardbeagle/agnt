// Three-page demo app for navigate-defect: dashboard → invoices → invoice
// detail with a planted defect (line items sum to $1,650, total says $2,100).
// Loopback only, fixed port so the proxy target is stable.
import http from 'node:http';

const PORT = 8023;

const shell = (title, body) => `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>${title}</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  :root { --bg:#0f1117; --panel:#171a23; --line:#262b38; --txt:#e6e9ef; --mut:#9aa3b2; --acc:#4f8cff; }
  * { margin:0; box-sizing:border-box; }
  body { font-family:-apple-system,'Segoe UI',Roboto,sans-serif; background:var(--bg); color:var(--txt); }
  main { max-width:880px; margin:0 auto; padding:32px 24px; }
  h1 { font-size:22px; margin-bottom:6px; }
  .sub { color:var(--mut); font-size:14px; margin-bottom:26px; }
  .crumb { font-size:13px; color:var(--acc); margin-bottom:18px; }
  .panel { background:var(--panel); border:1px solid var(--line); border-radius:12px; padding:20px; margin-bottom:20px; }
  table { width:100%; border-collapse:collapse; font-size:14px; }
  th, td { text-align:left; padding:9px 10px; border-bottom:1px solid var(--line); }
  th { color:var(--mut); font-weight:600; font-size:12px; text-transform:uppercase; letter-spacing:.04em; }
  td.num, th.num { text-align:right; }
  a.row { color:var(--txt); text-decoration:none; }
  tr:hover td { background:rgba(255,255,255,.02); }
  .total-row td { font-weight:700; border-bottom:none; font-size:15px; }
  .btn { display:inline-block; background:var(--acc); color:#fff; border:none; border-radius:8px; padding:10px 18px; font-size:14px; font-weight:600; cursor:pointer; }
  .chip { font-size:11.5px; padding:3px 9px; border-radius:99px; }
  .chip.overdue { background:rgba(239,68,68,.15); color:#f87171; }
  .chip.sent { background:rgba(79,140,255,.15); color:#93c5fd; }
</style></head><body><main>${body}</main></body></html>`;

const pages = {
  '/': shell('Acme — Dashboard', `
    <h1>Dashboard</h1>
    <p class="sub">Good morning, Riley.</p>
    <div class="panel">
      <table>
        <tr><th>Outstanding</th><th class="num">Amount</th></tr>
        <tr><td><a class="row" href="/invoices">3 unpaid invoices →</a></td><td class="num">$4,650</td></tr>
      </table>
    </div>`),

  '/invoices': shell('Acme — Invoices', `
    <div class="crumb">Dashboard / Invoices</div>
    <h1>Invoices</h1>
    <p class="sub">3 unpaid</p>
    <div class="panel">
      <table>
        <tr><th>Invoice</th><th>Customer</th><th>Status</th><th class="num">Total</th></tr>
        <tr><td><a class="row" href="/invoices/INV-1042">INV-1042</a></td><td>Globex Corp</td><td><span class="chip overdue">Overdue</span></td><td class="num">$2,100</td></tr>
        <tr><td><a class="row" href="/invoices/INV-1043">INV-1043</a></td><td>Initech</td><td><span class="chip sent">Sent</span></td><td class="num">$1,750</td></tr>
        <tr><td><a class="row" href="/invoices/INV-1044">INV-1044</a></td><td>Soylent</td><td><span class="chip sent">Sent</span></td><td class="num">$800</td></tr>
      </table>
    </div>`),

  '/invoices/INV-1042': shell('Acme — INV-1042', `
    <div class="crumb">Dashboard / Invoices / INV-1042</div>
    <h1>INV-1042 — Globex Corp</h1>
    <p class="sub">Issued May 12 · <span class="chip overdue">Overdue</span></p>
    <div class="panel" id="line-items">
      <table>
        <tr><th>Line item</th><th class="num">Amount</th></tr>
        <tr><td>Platform subscription — May</td><td class="num li">$1,200</td></tr>
        <tr><td>Overage — API calls</td><td class="num li">$450</td></tr>
        <tr class="total-row"><td>Total due</td><td class="num" id="total">$2,100</td></tr>
      </table>
    </div>
    <button class="btn">Send reminder</button>`),
};

http.createServer((req, res) => {
  const page = pages[new URL(req.url, 'http://x').pathname];
  if (!page) { res.writeHead(404); res.end('not found'); return; }
  res.writeHead(200, {'Content-Type': 'text/html'});
  res.end(page);
}).listen(PORT, '127.0.0.1', () => console.log(`nav upstream on http://127.0.0.1:${PORT}/`));
