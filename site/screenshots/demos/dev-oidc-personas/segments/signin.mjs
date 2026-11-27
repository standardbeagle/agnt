// Dev OIDC over the tailnet: the story app on build1 signs in through agnt's
// dev issuer. The browser reaches the tailnet-bound proxy, so the issuer
// identifies the viewer with `tailscale whois` and shows the persona picker
// (default-persona does not apply off-loopback). Then the indicator's persona
// chip switches to admin: the real switch POST, the same one `:as` and the
// `devauth` tool make.
//
// build1's proxy is shared with a live session, so this segment only uses
// page-local toasts — nothing goes over the daemon socket to other browsers.
export default async function run(d) {
  const pg = d.page;
  const mountRoot = () => pg.evaluateHandle(() => window.__devtoolGetMountRoot());

  // The indicator's output preview mirrors the live agent session on build1;
  // keep it out of the recording.
  const hidePreview = () => pg.evaluate(() => {
    const root = window.__devtoolGetMountRoot && window.__devtoolGetMountRoot();
    if (!root || root.getElementById('__demo-hide-preview')) return;
    const s = document.createElement('style');
    s.id = '__demo-hide-preview';
    s.textContent = '#__devtool-output-preview{display:none!important}';
    (root.host ? root : document.head).appendChild(s);
  }).catch(() => {});
  const ready = async () => {
    await pg.waitForFunction(() => window.__devtool && window.__devtool.indicator && window.__devtool.toast, undefined, {timeout: 30000});
    await hidePreview();
  };

  await ready();
  await d.sleep(1800);

  await pg.getByText('Continue as dev persona').click();
  await pg.waitForURL(/__agnt\/oidc\/authorize/, {timeout: 30000});
  await pg.waitForSelector('text=Dev Standard');
  d.mark('picker');
  await d.sleep(7500);

  await pg.getByText('Dev Standard').first().click();
  await pg.waitForURL((u) => u.pathname === '/', {timeout: 60000});
  await pg.waitForSelector('text=Welcome back, Dev Standard', {timeout: 60000});
  await ready();
  d.mark('standard');
  await d.sleep(3500);

  // Persona chip in the indicator panel.
  await pg.evaluate(() => { window.__devtool.indicator.show(); window.__devtool.indicator.togglePanel(true); });
  await d.sleep(1200);
  const root = await mountRoot();
  const chip = await root.evaluateHandle((r) => r.querySelector('.__devtool-persona-button'));
  await chip.asElement().click();
  d.mark('chip');
  await d.sleep(2600);
  const admin = await root.evaluateHandle((r) => r.querySelector('.__devtool-persona-menu [data-persona="admin"]'));
  await admin.asElement().click();

  // The switch expires the app session and sends it to login-path; the app's
  // sign-in button then completes silently as the new persona.
  await pg.waitForURL(/\/auth\/signin/, {timeout: 60000});
  await ready();
  await d.sleep(900);
  await pg.getByText('Continue as dev persona').click();
  await pg.waitForURL((u) => u.pathname === '/', {timeout: 60000});
  await pg.waitForSelector('text=Welcome back, Dev Admin', {timeout: 60000});
  await ready();
  d.mark('admin');
  await d.toast('Signed in as Dev Admin — same switch as :as admin in the overlay, or devauth {action:"as"} for an agent.', 'success', 6500);
  await d.sleep(7000);
}
