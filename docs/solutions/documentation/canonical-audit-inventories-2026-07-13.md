---
written_at: 2026-07-13T11:49:08Z
source_event: task:01KWMAX9F8MQAJCB4WYN2G6CAW
module: documentation
category: workflow-issues
confidence: high
sources:
  - workflow:01KXDMB6ZTQNE3SZGA46AWBTH5#attempt:01KXDMMYGEZJJN3J9AC1B9KFTS
  - workflow:01KXDMB6ZTQNE3SZGA46AWBTH5#attempt:01KXDMS2VJSKH8X492XNTTPYS5
  - git:05dc3e47
  - git:947db685
tags: [rewind, audit, canonical-inventory, deferred-work, traceability]
status: steering
recurrence: 1
---

# Audits must update canonical inventories

## Lesson

An audit is complete only when classifications land in the document's named canonical inventory and every deferred verdict cites its live follow-up task ID.

## What didn't work

The initial dated sweep classified the new sites but left accepted behaviors outside the canonical escape-hatch tables and described deferred work without task IDs. The first correction then created a second accepted-behavior table near the changed subsystem instead of updating the existing canonical table, leaving competing inventories.

## Why it recurs

Feature-local sections are convenient places to explain a decision, but future audits and agents follow the established canonical heading. A nearby duplicate looks complete in the diff while silently fragmenting the source of truth. Likewise, a prose-only “deferred” label loses the executable work item.

## Apply when

Extending platform, security, architecture, compatibility, or risk audits that already define classification tables, exception registries, or deferred-work conventions.

## Prevention

Before editing, locate the exact canonical headings named by the audit procedure. Merge new classifications there, search for duplicate inventories before review, and require every `sub-task filed` or equivalent verdict to include the full task ID plus rationale.
