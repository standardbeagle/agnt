---
title: Session-owned cross-layer publication
date: 2026-07-13
category: best-practices
module: remote-ssh
problem_type: best_practice
component: service_object
severity: high
applies_when:
  - Publishing transient client state into daemon-owned UI or diagnostic surfaces
  - Replacing a legacy transport path with a reconnect-capable durable owner
tags: [remote-ssh, session-ownership, authoritative-snapshot, event-routing, concurrency]
written_at: 2026-07-13T15:30:00Z
source_event: [task:01KX8R68TK3V37MKXZ3PWMZX5N, git:d994e66f, git:07bf8574, git:7714e91b]
---

# Session-owned cross-layer publication

## Context

Remote SSH forwarding exposed a recurring cross-layer failure mode: correct protocol and UI code can remain inert when wired into a legacy helper instead of the durable owner used by production. Transient snapshots can also outlive their publisher, while separately resolved side effects can split across targets during concurrent registry churn.

## Guidance

Treat publication as one ownership chain:

1. Trace the real entry point to the durable runtime owner before adding callbacks; tests must exercise that active path, not a same-shaped legacy helper.
2. Publish complete snapshots after reconciliation and every change. Key them to the publisher's registered daemon session, replace rather than merge, and remove them on explicit stop and connection cleanup.
3. Resolve implicit context before constructing events. For example, an omitted SSH path must become the resolved, normalized remote project root—not an empty placeholder.
4. Resolve a typed event's target once. Emit its browser toast and diagnostic through the same resolved server pointer, yielding either zero deliveries for ambiguous/missing scope or exactly one pair.

Keep protocol DTOs below daemon policy and overlay rendering: protocol carries data, daemon owns lifecycle and target resolution, and overlay consumes daemon DTOs. This preserves the daemon-to-overlay dependency boundary.

## Why This Matters

These rules make removal authoritative, prevent stale state after disconnect, avoid project leakage, and keep paired user/agent surfaces consistent even when proxy registries change concurrently. They also catch the deceptively common case where focused tests pass against code production never calls.

## When to Apply

- Adding status snapshots, inventories, or panels sourced by a reconnecting client.
- Migrating legacy notifications to typed daemon events.
- Routing one logical event to multiple side effects.
- Supporting optional CLI context that becomes concrete only after remote discovery.

## Examples

Bad: a legacy forwarding helper publishes mappings by a caller-provided key, while the active reconnect owner still sends an old toast.

Good: the active owner registers a daemon session, publishes full mapping snapshots through its on-change callback, and daemon cleanup deletes that session's snapshot. A developer event carries the resolved project and optional proxy ID; daemon resolves one proxy once and emits the toast/diagnostic pair on it.

## Related

- [SSH transport lessons](../../../.claude/rules/lessons-ssh-transport.md)
- [Daemon architecture](../../../.claude/rules/daemon-architecture.md)
- Task `01KX8R68TK3V37MKXZ3PWMZX5N`; commits `d994e66f`, `07bf8574`, `7714e91b`
