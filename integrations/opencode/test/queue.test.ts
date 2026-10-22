import assert from "node:assert/strict";
import { test } from "node:test";
import { compactionContext, decide, DigestQueue, meetsFloor, type Digest } from "../src/queue.ts";

const digest = (level: string, over: Partial<Digest["payload"]> = {}): Digest => ({
  type: "incident_digest",
  session_code: "s1",
  level,
  message: "x",
  payload: { summary: { error: 1, new: 1 }, ...over },
});

test("severity floor drops info digests", () => {
  const q = new DigestQueue("warning", 1000);
  assert.equal(q.add(digest("info")), false);
  assert.equal(q.hasPending(), false);
  assert.equal(q.add(digest("error")), true);
  assert.equal(meetsFloor("critical", "error"), true);
});

test("a burst coalesces into one batch carrying the highest severity and newest totals", () => {
  const q = new DigestQueue("info", 1000);
  q.add(digest("warning", { summary: { warning: 1, new: 1 } }));
  q.add(digest("critical", { summary: { critical: 1, warning: 1, new: 2 } }));
  q.add(digest("error", { summary: { critical: 1, error: 1, warning: 1, new: 3 } }));
  const b = q.take();
  assert.ok(b);
  assert.equal(b.count, 3);
  assert.equal(b.level, "critical");
  assert.equal(b.summary.new, 3);
  assert.match(b.text, /crit=1 err=1 warn=1 \(new=3\)/);
  assert.match(b.text, /agnt_incidents/);
  assert.equal(q.take(), undefined, "taking clears the batch");
});

test("an identical batch inside the dedup window is dropped, later one is not", () => {
  let t = 0;
  const q = new DigestQueue("info", 1000, () => t);
  q.add(digest("error"));
  assert.ok(q.take());
  t = 500;
  q.add(digest("error"));
  assert.equal(q.take(), undefined);
  t = 1500;
  q.add(digest("error"));
  assert.ok(q.take());
});

test("activity-defer: busy is context only, idle follows resumeOnIdle", () => {
  assert.equal(decide({ hasBatch: false, busy: false, resumeOnIdle: true }), "none");
  assert.equal(decide({ hasBatch: true, busy: true, resumeOnIdle: true }), "context");
  assert.equal(decide({ hasBatch: true, busy: false, resumeOnIdle: true }), "prompt");
  assert.equal(decide({ hasBatch: true, busy: false, resumeOnIdle: false }), "context");
});

test("compaction context lists only unread incidents with the cursor", () => {
  assert.equal(compactionContext("c1", [{ severity: "error", read: true }]), undefined);
  const text = compactionContext(
    "2026-10-20T00:00:00Z",
    [
      { severity: "error", summary: "TypeError in app.js", count: 3, read: false },
      { severity: "warning", summary: "slow request", read: false },
      { severity: "info", summary: "seen", read: true },
    ],
    1,
  );
  assert.ok(text);
  assert.match(text, /unread: 2 \(cursor 2026-10-20T00:00:00Z\)/);
  assert.match(text, /\[error\] TypeError in app.js x3/);
  assert.match(text, /\(\+1 more\)/);
  assert.doesNotMatch(text, /seen/);
});
