import assert from "node:assert/strict";
import { test } from "node:test";
import { compactionContext, meetsFloor } from "../src/queue.ts";

test("severity floor ranks info < warning < error < critical", () => {
  assert.equal(meetsFloor("info", "warning"), false);
  assert.equal(meetsFloor("warning", "warning"), true);
  assert.equal(meetsFloor("critical", "error"), true);
  assert.equal(meetsFloor("bogus", "warning"), false, "unknown counts as info");
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
