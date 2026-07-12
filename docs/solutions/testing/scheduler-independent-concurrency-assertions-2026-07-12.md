---
written_at: 2026-07-12T07:00:00Z
source_event: task:01KX9RQJ2D76QE0GCRK1Q9VZ5F
module: testing
category: test-failures
confidence: high
sources:
  - workflow:01KXAFDRYDX3R2M2ESRK8DKNAP#correctness-review-attempt-1
  - git:e558f807
  - git:11f4224c
tags: [rewind, scheduler-independence, explicit-synchronization, wall-clock]
status: steering
recurrence: 1
---

# Scheduler-independent concurrency assertions

## Lesson

Prove concurrency and non-blocking behavior with observable state and test-controlled synchronization; use same-run baselines only when elapsed performance is itself the contract.

## What didn't work

The first router-isolation rewrite removed the tight elapsed threshold but retained a 5ms sleep to assume the slow handler had entered its blocking region. Correctness review rewound it because descheduling could let the fast batch finish before the slow handler entered, allowing globally serialized dispatch to pass.

## Why it recurs

Replacing one wall-clock threshold with another sleep preserves the hidden premise that the scheduler ran a goroutine promptly. Busy hosts violate that premise without violating product behavior.

## Apply when

Use this for tests claiming that work is asynchronous, one worker cannot block another, or callers return while background work remains blocked. Reserve baseline-relative timing for genuine performance comparisons where state ordering cannot express the contract.

## Prevention

Instrument fixtures with an `entered` signal and a test-owned `release` gate. Wait for `entered`, run the supposedly independent work, assert the blocked operation has not completed, then release it. For async-return tests, assert the returned state while the background action is observably in flight. For latency contracts, compare with an identical same-run control path and retain a small additive floor for timer granularity.
