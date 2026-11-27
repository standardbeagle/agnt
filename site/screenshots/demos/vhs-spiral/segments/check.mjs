// Post-attempt check beat: reload the raw page, apply the state the attempt
// left behind, hold long enough for the viewer to see nothing (or worse).
// args: {"state": "half" | "gone", "holdMs": 3200}
import {halfOff, gone} from './bugs.mjs';

export default async function run(d, args) {
  const form = d.page.locator('#customer-form');
  await form.scrollIntoViewIfNeeded();
  await d.page.evaluate(args.state === 'gone' ? gone : halfOff);
  d.mark('state-shown');
  await d.sleep(args.holdMs ?? 3200);
}
