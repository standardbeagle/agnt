---
written_at: 2026-07-13T08:17:57Z
source_event: task:01KX9F910CQCTMY6PA0NF5P82Z
module: documentation
category: workflow-issues
confidence: high
sources:
  - workflow:01KXD8642NQY2S4SWW4J1ZNKP0#attempt:01KXD8D6E8JP1F3ZNHYH096P9M
  - git:a49f42c57ace8a8fd6e55ba097aaef7f934588fe
tags: [rewind, missing-criteria, performance-contract, documentation-consistency]
status: steering
recurrence: 1
---

# Repeated performance contracts must remain exact

## Lesson

When a performance contract appears in both detailed documentation and repository guidance, every normative repetition must distinguish the unloaded target from the CI-enforced guard and retain the guard's exact formula and purpose.

## What didn't work

The first edit made the detailed hook documentation exact but reduced the repository guidance to a vague “baseline-relative bound.” Correctness review rewound the task because that repetition omitted `hook_p99 <= 4 * same-run baseline_p99 + 50ms` and its gross-regression purpose.

## Why it recurs

Summaries tend to compress formulas into prose, which can turn an explicitly load-calibrated test contract into an apparently absolute performance guarantee or an unverifiable vague claim.

## Apply when

Editing a target, SLO, benchmark result, or test-enforced performance bound that is repeated across overview and detailed documentation.

## Prevention

Make acceptance criteria enumerate every normative repetition. Require each one to state target versus enforced guard, the exact bound, and why the guard differs from the target; review all repetitions against the implementing test.
