---
paths:
  - "**/*_test.go"
---

# Timing assertions

Judge time-driven event assertions (ticker fires, keepalives, heartbeats, polling) by the **recorded actual timestamps** of what happened, never by the configured interval times assumed scheduler promptness. Example: a keepalive is a bug only if it lands between two sends whose own observed gap was shorter than the interval (`TestEventHub_KeepaliveHeartbeat`). A longer fixed sleep only moves the flake window.

Rules:

- No absolute wall-clock value is a primary invariant. `require.Eventually` for generous headroom is fine.
- A fixed timeout guarding an eventual/liveness property is acceptable as a generous ceiling. A latency SLO must be baseline-calibrated in the same run (e.g. hook `p99 ≤ 4× baseline_p99 + 50ms`).
- Real-Chrome tests that starve under CPU oversubscription are `chromee2e`-tagged and run only on an unloaded machine. Widening their budget is mitigation, never a fix, and assertions are never weakened.
- A non-reproduced flake gets a defensive fix only when exactly one mechanism plausibly matches the symptom and the fix mirrors a proven sibling pattern (e.g. `findPortHoldersWithRetry` mirroring `killPortHoldersGuarded`'s re-scan). Label it non-reproduced-defensive, not root-caused.

Diagnosis procedure: `flake-triage` skill.
