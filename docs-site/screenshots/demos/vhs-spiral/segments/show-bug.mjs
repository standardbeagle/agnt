// Show the bug: the Add-customer form with its submit button half off-screen.
// Raw upstream page (no proxy) — the viewer sees exactly what the agent can't.
import {halfOff} from './bugs.mjs';

export default async function run(d) {
  const form = d.page.locator('#customer-form');
  await form.scrollIntoViewIfNeeded();
  await d.page.evaluate(halfOff);
  d.mark('bug-shown');
  await d.sleep(3600);
}
