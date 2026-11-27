// The browser view of the same tunnel. The engine opens the public hostname
// cold: Cloudflare's edge answers with the Access login. The segment then
// presents the demo's Access service token (headers read from the 0600 file,
// never rendered) and loads the page again: through the tunnel, past agnt's
// origin check, into the proxied app with the agnt bundle injected.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const HEADERS = path.join(os.homedir(), '.config/cloudflared/agnt-demo-access.headers');

export default async function run(d) {
  const pg = d.page;
  await pg.waitForURL(/cloudflareaccess\.com/, {timeout: 30000});
  d.mark('login');
  await d.sleep(4500);

  const headers = Object.fromEntries(fs.readFileSync(HEADERS, 'utf8').trim().split('\n')
    .map((l) => l.split(/:\s*/, 2)));
  await pg.route('https://agnt-demo.sbdev.io/**', (route) =>
    route.continue({headers: {...route.request().headers(), ...headers}}));
  await pg.goto('https://agnt-demo.sbdev.io/', {waitUntil: 'load'});
  // The proxy wraps the page: the app renders in agnt's content frame.
  await pg.frameLocator('iframe#__devtool_content_frame').getByText('Acme — staging build').waitFor({timeout: 30000});
  d.mark('app');
  await d.sleep(6000);
}
