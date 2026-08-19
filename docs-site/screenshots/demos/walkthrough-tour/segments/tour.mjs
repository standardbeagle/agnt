// The agent runs its guided tour and the camera records it — one authored
// script, not two. d.walkthrough() starts the SAME overlay the walkthrough MCP
// tool starts (PROXY EXEC of window.__devtool.walkthrough.start), and each step
// transition drops a `step:<n>` mark that narration and keep-ranges anchor to.
export default async function run(d) {
  await d.sleep(1200);

  await d.agentToast('Walking you through what shipped — three stops.', 'info', 3600);

  await d.walkthrough([
    {
      title: 'What shipped',
      body: 'Twelve merges landed this week.',
      target: '#changes',
      advance: {type: 'auto', ms: 4200},
    },
    {
      title: 'How it rolls out',
      body: 'Staged to 10% of dev machines first.',
      target: '#rollout',
      advance: {type: 'auto', ms: 4200},
    },
    {
      title: 'Promote when quiet',
      body: 'One quiet day in the error inbox and this button goes green.',
      target: '#promote',
      gesture: 'click',
      gesture_label: 'Click to promote the release',
      advance: {type: 'auto', ms: 4200},
    },
  ], {id: 'release-tour', title: 'This week in agnt'});

  // Each wait is on the overlay's own state, so the mark carries the real
  // transition time rather than a guessed sleep.
  await d.walkthroughStep(2);
  await d.walkthroughStep(3);
  await d.sleep(3800);

  await d.walkthroughStop();
  await d.sleep(1200);
}
