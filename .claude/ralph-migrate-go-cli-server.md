# Ralph Loop: go-cli-server Migration

## Objective

Complete the migration of agnt daemon to fully use go-cli-server's Hub for command dispatch, remove duplicate code, and ensure high test coverage.

## Current State Analysis

On each iteration, assess the current state by checking:

1. **Command Registration Status**
   - Check `internal/daemon/daemon.go` function `registerCommands()`
   - If it contains "TODO" or only logs debug messages, migration is INCOMPLETE

2. **Connection Handling**
   - Check if `internal/daemon/connection.go` still has its own command dispatch loop
   - If `handleCommand()` switch statement exists with all verbs, old path is STILL ACTIVE

3. **Duplicate Code**
   - Check if `internal/process/` directory exists (should be DELETED - use go-cli-server/process)
   - Check for duplicate scheduler, pidtracker, resilient client code in internal/daemon/

4. **Test Coverage**
   - Run `go test -cover ./internal/daemon/...`
   - Target: 80%+ coverage on new Hub-integrated code

## Migration Tasks (in order)

### Phase 1: Register Commands with Hub

1. In `daemon.go`, implement `registerCommands()` to register agnt-specific commands with Hub:
   ```go
   d.hub.RegisterCommand(hub.CommandDefinition{
       Verb:     "PROXY",
       SubVerbs: []string{"START", "STOP", "STATUS", "LIST", "EXEC", "TOAST"},
       Handler:  d.handleProxy,
   })
   ```

2. Register all verbs: PROXY, PROXYLOG, TUNNEL, CHAOS, CURRENTPAGE, OVERLAY, DETECT, SESSION

3. Adapt handlers in `handler.go` to match Hub's handler signature

### Phase 2: Remove Old Connection Command Dispatch

1. Remove the `handleCommand()` switch statement from `connection.go`
2. Remove verb-specific handlers from Connection (they should be on Daemon now)
3. Connection should only handle connection lifecycle, not command dispatch
4. Hub's built-in handlers should process: PING, INFO, SHUTDOWN, RUN, PROC

### Phase 3: Delete Duplicate Code

Check and delete if present:
- `internal/process/` directory (use go-cli-server/process instead)
- Duplicate scheduler code if go-cli-server provides it
- Duplicate resilient client if go-cli-server provides it

### Phase 4: Add Integration Tests

Create tests that verify:
1. Commands are dispatched through Hub (not old Connection path)
2. Hub's ProcessManager integration works
3. Session cleanup through Hub works
4. All agnt-specific commands work via Hub dispatch

Test file: `internal/daemon/hub_integration_test.go`

### Phase 5: Verify & Clean Up

1. Run full test suite: `go test ./...`
2. Run with race detector: `go test -race ./...`
3. Check coverage: `go test -cover ./internal/daemon/...`
4. Remove any commented-out old code
5. Update imports to use go-cli-server packages directly

## Success Criteria

ALL of the following must be true:

- [ ] `registerCommands()` registers all agnt commands with Hub
- [ ] `connection.go` no longer has command dispatch switch statement
- [ ] Hub handles command routing (verify with debug logs or tests)
- [ ] `internal/process/` directory does NOT exist (or is empty)
- [ ] Test coverage >= 80% on `internal/daemon/`
- [ ] `go test ./...` passes
- [ ] `go test -race ./...` passes
- [ ] No "TODO" comments related to go-cli-server migration remain

## Iteration Protocol

Each iteration:

1. **Assess**: Run checks above to determine current state
2. **Identify**: Find the NEXT incomplete task
3. **Implement**: Make ONE focused change
4. **Test**: Run relevant tests
5. **Verify**: Check if success criteria are closer to being met

Do NOT try to do everything at once. Make incremental progress.

## Completion Promise

When ALL success criteria are met, output:

```
<promise>GO-CLI-SERVER MIGRATION COMPLETE</promise>
```

Do NOT output this promise until you have verified ALL criteria are satisfied.

## Important Notes

- Refer to `docs/claude/go-cli-server-migration-plan.md` for detailed migration plan
- Check go-cli-server source in `go.mod` for available packages
- The Hub should own: socket, accept loop, client management, command dispatch
- Daemon should own: agnt-specific handlers, proxy manager, tunnel manager, session registry

## Anti-Patterns to Avoid

- Do NOT keep both old and new command paths active
- Do NOT add backwards compatibility shims
- Do NOT leave dead code "just in case"
- Do NOT skip writing tests for new code
- Do NOT mark tasks complete without verification
