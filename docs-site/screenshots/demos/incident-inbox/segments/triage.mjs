// Incident-context beat: the agent triages what just landed in the inbox.
// The errors are real (the monitor segment's curls produced them through this
// proxy); the agent's reply travels the real PROXY TOAST transport.
export default async function run(d) {
  const cf = d.cf();
  await cf.locator('#customer-form').scrollIntoViewIfNeeded();
  await d.sleep(1200);

  await d.agentToast('Inbox: 2 incidents while you watched — POST /api/customer 500 (Crash Test), 409 conflict (Globex).', 'warning', 5200);
  await d.sleep(3600);

  await d.toast('500: server-side crash on company="Crash Test" — payload is valid, the handler is not', 'error', 5000);
  await d.sleep(3400);
  await d.toast('409: duplicate company — client should pre-check before submit', 'warning', 4600);
  await d.sleep(3000);

  await d.agentToast('Neither needed you to paste a log line. Both arrived with request context attached.', 'success', 5200);
  d.mark('triaged');
  await d.sleep(3400);
}
