---
paths:
  - "internal/overlay/**"
  - "internal/sshclient/**"
  - "internal/daemon/**"
  - "internal/proxy/**"
  - "internal/tunnel/**"
  - "internal/browser/**"
  - "internal/chromedp/**"
---

# Lessons: budgets that expire mid-frame, registries that outlive their resource

## 1. A budget that runs out mid-unit discards the rest of that unit

Byte caps, lookaheads, timeouts and retry counts are fine. The defect is what the exhaustion branch does with a partial unit. Wrong answers: return what you have, or reset and hand the remainder to the next layer as fresh input. Right answers: discard to the end of the unit, or fail loud. Keep an outer cap on the discard path so an unterminated stream cannot wedge the reader.

Instances, all fixed: the 8KB URL scan window in `internal/daemon/urltracker.go` (`scanStreamForURLs` finishes the straddling line within `maxScanLineOverrun`, else discards it); ESC lookahead and `csiMaxLen` in `internal/overlay/input.go` (OSC/DCS/CSI tails leaked into the child as keypresses); `win32PendingFlush` in the same file (split sequences released early); the 4096-byte line accumulator in `internal/overlay/activity.go` (line tails reached the alert tap as new lines).

Tests: the split is the test case. Feed input in chunks, across read boundaries, with gaps. A single write passes against broken code. Record honest limits as tests too (`TestIntroducerSeparatedFromItsSequenceDegradesToPassthrough`). Read HTTP responses to their end (`http.ReadResponse`, `io.ReadAll`), never judge them from one `Read`.

## 2. Retire a registry entry after the resource, by the identity you resolved

Applies to any registry backing a user-visible status report (see `daemon-architecture.md` § Data Ownership).

1. Remove the entry only after the resource is really gone (port forwards were deleted before closing, so status showed live listeners as finished).
2. Remove the resource you resolved, not the string the caller typed (`Stop("dev")` resolved a compound id, then deleted key `"dev"`).
3. Guard the delete by identity so a close cannot retire a same-id replacement: `sync.Map.CompareAndDelete`, or `ReplaceExact`/`UnregisterExact` in `internal/daemon/session.go`.
4. Whatever registers on start retires on stop and refreshes on restart.

A new registry's doc comment states who reads it and whether that reader is user-visible.

## 3. Test isolation does not reach a child process

`t.TempDir()`, `t.Setenv` and `os.Chdir` constrain only the test process. A fixture that execs anything sets `cmd.Dir`. A test that builds a binary builds it fresh, into a temp dir, once per test binary.

## Fixed from the sweep register

- Frames and HTTP stubs read to their terminator: `hub_router_stress_test.go` (`readFrameUntilTerminator`), `e2e_autostart_test.go` (`http.ReadRequest`).
- Tunnel, browser and chromedp managers count an entry from the moment it is listed and retire it by identity (`CompareAndDelete`).
- `ProcessAutoRestarter.Take` reads and removes one registration; `Unregister` is a stop intent for whatever the id holds.
- `sendEntersUntilActivity` stops retrying once the screen shows the message left the input box (`ActivityMonitor.CursorRowsContain`); silence alone still retries when the screen cannot tell.
- `utils.js` ancestor walks (`MAX_ANCESTOR_WALK`) report a truncated walk instead of answering as if they reached the root.
- `alert_delivery.go` defers while the agent is active up to `maxActivityDeferral` (2 min), not five 2s retries.
- `cmd/agnt/ai_test.go` runs the real `claude` in a temp dir with an empty `CLAUDE_CONFIG_DIR` (`isolateClaude`).

## Open register (found by sweep, not yet fixed)

None.
