---
name: security-boundary-review
description: "Review a change touching a security/tenancy boundary, SSRF/CSP/auth guard, or a doc claiming a security control. Use when: reviewing or testing a guard, writing security docs, planning a boundary slice."
---

# Security boundary review

A green suite and the author's self-review are necessary, not sufficient. Both rewinds on the publish epics landed on code that was already green. Attack the boundary the author's tests assume. Code-level rules live in `.claude/rules/publish-security-review-lessons.md`.

## Reviewing a guard or boundary change

1. **Read the cited mechanism.** Do not trust the description or the spec. Example: a claim of "replaces upstream CSP" pointed at a strip-merge that kept hostile directives.
2. **Attack the collision path, not the naming path.** Tests often prove that you cannot name another tenant's resource. Also test what happens when two tenants' keys, digests or hashes collide.
3. **Assert the refusal's provenance.** Ask: "If I deleted this control, would this assertion still pass?" In a layered path it usually would. Assert both of these:
   - the error names the control's verdict, e.g. `"upstream refused"`;
   - the dangerous side effect was never reached, e.g. the dialer was called exactly once.
4. **Mutation-verify once.** Revert the fix or break the guard, rerun, and confirm the test fails on a behavioural assertion rather than on a key-comparison line. Restore the tree afterwards.
5. **Assert coincidence premises.** If a test depends on equal digests, colliding keys or the same timestamp, assert that premise first with `t.Fatalf("premise broken: ...")`.
6. **Never pin a value by the symbol that produces it.** Spell the expected security constant as a literal. A test that compares against `proxiedUpstreamStyleSrc` passes any mutation of it.
7. **Name the field you mean.** Assert on the directive or field itself, e.g. `script-src` sources are all hashes. A document-wide substring such as "no `unsafe-inline` anywhere" breaks the day an unrelated directive legitimately gains it.

## Security docs

- A sentence describing a shipped control's scope is load-bearing. Grep the guard's production callers and check the claim against the call sites. The count is usually one.
- The tell is coverage words: "every", "all", "the same guard also".
- When CSP-adjacent behaviour is enforced at two layers (validator or renderer first, then the header), state which one refuses first.
- One false claim obliges a whole-doc sweep for that claim's shape. Fixing only the cited line blesses its siblings.
- Fix a false claim by naming the real gap, the silent failure it hides, and the tracking task id. Never delete the sentence.

## Briefs and fix_hints

- **Dispatching:** mark your understanding as a hypothesis, and tell the subagent to verify it against the code and report contradictions.
- **Reworking:** a fix_hint's mechanism claim is a diagnosis, not a spec. Follow the code, and record any divergence in the commit body.
- **Reviewing:** verify the mechanism before you write the hint.

## Planning a boundary slice

- **Headline coverage:** check that the slice set covers the DoD's headline capability. If the decomposition narrows it, write the narrowing down as a scope decision.
- **Owning file in scope:** a criterion that names a test tier, config, doc or generated asset needs its owning file in the task's `fileScope`. For example, real-Chrome assertions need the `*_e2e_test.go` file with the `chromee2e` tag.
- **Integration points exist:** verify that named integration points exist and sit on the claimed path. For the public plane, check `moduleRole` and `rolePublicModules` membership.
- **Impossible slice:** if a slice cannot be done as written, STOP and return a re-scope report. Never widen scope silently or fake a substitute.
- **Traceability table as tie-breaker:** when the spec and a decision appear to conflict, consult the spec's traceability table before escalating.
- **Irreversible batch operations:** phrase the criterion structurally, e.g. "the destructive call is unreachable from a partial scan, verified by reading every call site."
- **Hazards for future consumers:** when you spot a hazard for a consumer that does not exist yet, hand it forward as an explicit constraint on that consumer's task.
