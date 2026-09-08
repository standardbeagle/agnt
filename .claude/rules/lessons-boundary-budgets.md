# Lessons: budgets that expire mid-frame, and registries that outlive what they describe

Written after sweeping the whole tree for siblings of four fixes landed on
2026-09-07 (`071028d7`, `009487c7`, `d7a617cf`, `bc524293`). The sweep found
the same two shapes in five more places, which is why they are written down
as rules rather than as four commit messages.

## 1. A budget that expires mid-frame must discard, never reinterpret or forward

Every layer that reads a stream has a budget: a byte cap, a lookahead, a
timeout, a retry count. The defect is never the budget. It is what happens
when the budget runs out **partway through one logical unit** — and the
answer must be to drop the rest of that unit, never to treat what arrived as
complete, and never to hand the remainder to the next layer as fresh input.

Confirmed instances, all with the same user-visible ending — text the child
never received as input appearing in the agent's prompt, or an alert the
child never printed:

| Budget | Was | Is |
|---|---|---|
| One byte of lookahead after ESC (`internal/overlay/input.go`) | An OSC/DCS/APC/PM reply read as `Escape+]`, closing the panel; the payload typed into the child | Consumed to its string terminator (ST or BEL) and discarded |
| `csiMaxLen` = 32 (same file) | Reader returned to ground mid-sequence; the tail arrived as keypresses, `q`/`x` closing the panel | Stays inside the sequence, dropping to its own final byte |
| `win32PendingFlush` = 25ms (same file) | Any held remainder released, so a split `\x1b[1;5C` stopped matching its binding and was typed into the child | Only a bare ESC keeps the short budget; a remainder past the introducer waits 20x longer |
| 4096-byte line accumulator (`internal/overlay/activity.go`) | Buffer reset; the tail of one printed line delivered to the alert tap as a line of its own | Keeps the prefix, drops the rest of that line |
| One `Read` = one response (tests) | A relayed HTTP response judged from one segment; a stalled connection required to fail its *first* read | Read to the message's own end (`http.ReadResponse`, `io.ReadAll`) |

The tell when hunting these: find the exhaustion branch and ask what it does
with the bytes it has. `return what we have` and `reset and carry on` are the
two wrong answers. `discard to the end of this unit` and `fail loud` are the
right ones. An outer cap still belongs on the discard path so a stream that
never terminates cannot wedge the reader.

Corollary for tests: **the split is the test case.** Written as one write, the
line-accumulator test passes against the broken code, because the cap was only
applied between writes. Feed the input the way the transport delivers it —
in chunks, at a read boundary, with a gap — or the test proves nothing.

Second corollary: the honest limit belongs in a test too. A lone Escape and the
first byte of a sequence are the same byte, so an introducer that arrives alone
cannot be reassembled. `TestIntroducerSeparatedFromItsSequenceDegradesToPassthrough`
records that, so the next reader does not read it as a regression.

## 2. A registry that backs a status report is retired after the resource, and by identity

`.claude/rules/daemon-architecture.md` § Data Ownership says daemon state is a
cache and OS truth is authoritative. This is the ordering half of that rule,
and it has now been broken four times in three subsystems.

Three requirements, all of which have failed independently:

1. **Remove after the resource is really gone.** The port forwards deleted from
   the registry and then closed, so `agnt ssh --status` and the ports panel
   reported a forward as finished while its listener still accepted
   connections (`009487c7`).
2. **Remove the resource you resolved, not the string the caller typed.**
   Proxy commands accept one component of a compound id, so `Stop("dev")`
   resolved `myapp-abc1:dev:localhost-3000` and then deleted the key `"dev"`,
   which matched nothing. One restart left two rows for one proxy, one of them
   dead, and the active count never came down.
3. **Guard the delete by identity.** A resource created again under the same id
   while the old one is closing must not be retired by the close it does not
   belong to. `sync.Map.CompareAndDelete`, or the `ReplaceExact`/`UnregisterExact`
   pattern in `internal/daemon/session.go`.

And the mirror image: **whatever registers on start must retire on stop.** The
proxy admin row the overlay status bar and SCRIPT LIST read had no removal on
the stop path at all, and no refresh on restart, so it kept naming an address
nothing was bound to.

When adding a registry, answer in its doc comment: who reads it, and is that
reader user-visible? If it is, the ordering above is a contract, not a
preference.

## 3. A test's isolation does not reach a child process

Already recorded for `cmd.Dir` in
`.claude/rules/testing-parallel-package-flakes.md`. The sweep found three more
SSH fixture handlers exec'ing real commands with no `cmd.Dir`, and one E2E
harness building its binary into the repository root **and reusing whatever it
found there** — the stale-binary version-skew trap `lessons-ssh-transport.md`
§1 already documents, rebuilt from scratch in another package.

`t.TempDir()`, `t.Setenv` and `os.Chdir` constrain the test process. A child
re-resolves every relative path and reads every variable itself. A fixture that
execs anything needs `cmd.Dir`, and a test that builds a binary builds it fresh,
into a temp dir, once per test binary.

## Register: found by the sweep, not yet fixed

Ranked, with the reason each was left. None are believed to be actively
breaking a shipped path today.

- `internal/daemon/hub_router_stress_test.go:494` — a length-prefixed protocol
  frame parsed from one 1024-byte `Read`. Class 1, test only.
- `internal/daemon/e2e_autostart_test.go:3167` — upstream stub replies after one
  `Read` of a request relayed through the proxy, and closes with unread bytes
  (RST rather than FIN). Class 1, test only.
- `internal/tunnel/manager.go:51`, `internal/browser/manager.go:52`,
  `internal/chromedp/manager.go:112` — registered before `Start`, so the list
  contains an entity the active count says does not exist for the whole start
  window. Mitigated by an honest `State` on each row.
- `internal/daemon/process_autorestart.go:243` — `Unregister` deletes by id
  without the identity guard its sibling `removeIfCurrent` documents.
- `cmd/agnt/overlay.go:837` — `sendEntersUntilActivity` retries on "no output
  observed", which is not "not accepted"; exhaustion has sent up to four bare
  Enters into whatever the agent was showing.
- `internal/daemon/urltracker.go:163` — the 8KB scan window ends at a byte
  offset and the cursor advances past it, so a URL straddling the boundary can
  be registered truncated and the real one never re-scanned.
- `internal/proxy/scripts/utils.js` — `generateSelector`, `getStackingContext`
  and `isDevtoolElement` all cap at 50 ancestors and return their *ordinary*
  answer on exhaustion (an unanchored selector, `documentElement`, `false`),
  which the agent then acts on as if it were the real one.
- `internal/overlay/alert_delivery.go:77` — after five defers the batch is
  delivered into an agent that is still active, the condition the gate exists
  to avoid.
- `cmd/agnt/ai_test.go` — spawns the real `claude` CLI with the developer's own
  `~/.claude` and credentials, inside the repo, then kills the group.

<!-- provenance: written_at 2026-09-07; source_event the sweep for siblings of
     commits 071028d7, 009487c7, d7a617cf, bc524293; fixes landed as
     3261e372 (RED), 0943f68e, 10dcd58e, be1986a3, 72e0ae4d, 09b113a4,
     7135f7de -->
