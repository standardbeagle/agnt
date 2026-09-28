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

Instances, all fixed: ESC lookahead and `csiMaxLen` in `internal/overlay/input.go` (OSC/DCS/CSI tails leaked into the child as keypresses); `win32PendingFlush` in the same file (split sequences released early); the 4096-byte line accumulator in `internal/overlay/activity.go` (line tails reached the alert tap as new lines).

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

## Open register (found by sweep, not yet fixed)

- `internal/daemon/hub_router_stress_test.go:494`: frame parsed from one 1024-byte `Read` (class 1, test only).
- `internal/daemon/e2e_autostart_test.go:3167`: stub replies after one `Read` and closes with unread bytes (class 1, test only).
- `internal/tunnel/manager.go:51`, `internal/browser/manager.go:52`, `internal/chromedp/manager.go:112`: registered before `Start`, so the list and the active count disagree during startup (mitigated by an honest `State`).
- `internal/daemon/process_autorestart.go:243`: `Unregister` deletes by id without the identity guard `removeIfCurrent` has.
- `cmd/agnt/overlay.go:837`: `sendEntersUntilActivity` treats "no output" as "not accepted" and can send up to four stray Enters.
- `internal/daemon/urltracker.go:163`: the 8KB scan window ends at a byte offset, so a URL straddling it can register truncated.
- `internal/proxy/scripts/utils.js`: `generateSelector`, `getStackingContext` and `isDevtoolElement` return ordinary answers after hitting the 50-ancestor cap.
- `internal/overlay/alert_delivery.go:77`: after five defers, the batch is delivered into a still-active agent.
- `cmd/agnt/ai_test.go`: spawns the real `claude` CLI with the developer's `~/.claude`, inside the repo.
