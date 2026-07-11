# Walkthrough: Hardening a Dashboard's Error Handling with Chaos Injection

## What it is

Chaos injection is a fault layer built into the agnt reverse proxy. It sits
between the browser and your backend and deliberately breaks responses — adds
latency, returns 500s, drops connections, truncates bodies, reorders concurrent
responses — so you can see how your frontend behaves when the network and the
API misbehave. You drive it entirely through the `proxy` MCP tool with
`action: "chaos"`.

## Why it's unique

Two properties make agnt's chaos layer genuinely useful for hardening, not just
breaking things:

- **Deterministic, seeded faults.** When you set a seed, the fault stream is a
  SplitMix64 counter sequence: draw *n* is `mix(seed + n·gamma)`. Serially (the
  case that matters for a scripted test), the *same seed produces the same
  faults* — so you can reproduce a failure, fix it, and re-run the identical
  fault sequence to prove the fix. Unseeded, it uses the process-global PRNG for
  ordinary "just make it flaky" use.
- **Swallowed-error detection.** The proxy can flag injected faults that your app
  *ate silently* — an injected 500 that produced no app-side error surfaces as an
  incident. This catches the worst class of dashboard bug: the fetch that fails,
  the `.catch(() => {})` that hides it, and the widget that just renders stale or
  blank with no user-visible error.

## Real-world scenario

A metrics dashboard makes a dozen concurrent API calls on load. On a good
network it looks fine. In the field: one endpoint 500s intermittently and the
chart silently shows last-hour data forever; a slow endpoint makes two panels
race and render out of order; a dropped connection leaves a spinner spinning
forever. You want to surface every one of these, add proper error/retry UI, and
prove the fixes hold — reproducibly.

## Step by step

### 1. Start (or identify) the proxy

Chaos operates on a running proxy. Use its id (`dash` below).

### 2. See what's there

The default chaos operation is `status`:

```
proxy {action: "chaos", id: "dash"}
```

This reports whether chaos is enabled plus current stats and rules. (There is no
`get` operation — `status` is the read surface.)

### 3. Apply a preset to start breaking things

Presets are ready-made fault bundles. List them:

```
proxy {action: "chaos", id: "dash", chaos_operation: "preset"}
```

Then apply one that matches your fear. For an unreliable backend:

```
proxy {action: "chaos", id: "dash", chaos_operation: "preset", chaos_preset: "flaky-api"}
```

`flaky-api` injects a 5% rate of `500/502/503/504`, variable latency on 30% of
requests, and a 2% timeout rate. Other built-ins include `mobile-3g`,
`mobile-4g`, `race-condition` (out-of-order responses — the panel-race bug),
`stale-tab`, `slow-connection`, `connection-drops`, `rate-limited`,
`auth-failures`, `service-degradation`, and `pressure-test`. Applying a preset
enables chaos.

### 4. Make it deterministic so failures are reproducible

Presets don't set a seed by default (unseeded = process-global PRNG). To get a
reproducible fault stream, push a config with a seed via the `set` operation —
this is what turns "flaky sometimes" into "flaky the *same way* every run":

```
proxy {action: "chaos", id: "dash", chaos_operation: "set", chaos_config: {
  enabled: true,
  seed: 42,
  rules: [
    { id: "err", name: "500s on metrics", type: "http_error",
      url_pattern: "/api/metrics", probability: 0.3, error_codes: [500] },
    { id: "lat", name: "slow metrics", type: "latency",
      url_pattern: "/api/metrics", min_latency_ms: 200, max_latency_ms: 2000 }
  ]
}}
```

With `seed: 42`, a serial reload produces exactly the same 500s and delays every
time. Fix your code, reload against the same seed, and any change in behavior is
your fix — not chaos noise.

You can also add or remove individual rules without replacing the whole config:

```
proxy {action: "chaos", id: "dash", chaos_operation: "add_rule", chaos_rule: { … }}
proxy {action: "chaos", id: "dash", chaos_operation: "remove_rule", chaos_rule_id: "err"}
```

### 5. Reload the dashboard and watch it fail

With the proxy injecting faults, load the dashboard. The latency + 500 injection
on `/api/metrics` should expose the silent-stale-chart bug and any panel that
races. Injected HTTP errors and latency are counted in stats.

### 6. Catch the errors your UI eats

Enable swallowed-error detection (a runtime toggle, controlled from the browser
chaos panel, not config). When on, an injected fault that produces *no* app-side
error is raised as an incident — so the chart that silently kept stale data is no
longer invisible. This is the step that finds the bugs you didn't know to look
for.

### 7. Fix, then verify against the same seed

Add the missing error toasts, retry logic, and loading/empty states. Then re-run
with the *same seed* from step 4. Because the seeded stream is reproducible
serially, you're replaying the identical fault sequence — if the previously-stale
chart now shows an error and retries, the fix is proven against the exact failure
that exposed it, not a new random draw.

### 8. Clean up

```
proxy {action: "chaos", id: "dash", chaos_operation: "clear"}
```

`clear` removes all rules, disables chaos, and resets stats. `disable` /
`enable` flip injection on and off without discarding your config.

## Gotchas

- **`id` is required for every chaos operation.** No id → error.
- **The operations are a fixed set:** `enable`, `disable`, `status`, `set`,
  `preset`, `add_rule`, `remove_rule`, `list_rules`, `stats`, `clear`. There is
  no `get` — read state with `status` or `stats`. An empty `chaos_operation`
  defaults to `status`.
- **Determinism is per-draw-count, not per-wall-clock-order.** Under *concurrent*
  load, requests interleave their shared atomic counter nondeterministically, so
  a seeded run is only bit-for-bit reproducible when draws are serial (scripted
  reloads, tests). Don't expect two parallel-heavy loads to be identical.
- **A seed of 0 means unseeded.** Zero is the sentinel for "use the process-global
  PRNG," so `seed: 0` does *not* give you determinism — pick any non-zero seed.
- **Swallowed-error detection is a runtime toggle, not a config field.** It's set
  from the chaos panel at runtime and read at the injection site; it won't appear
  in a `chaos_config` you push via `set`.
- **`probability` defaults to 1.0.** A rule with no `probability` fires on every
  matching request. Set it below 1.0 for intermittent faults.
- **Applying a preset enables chaos.** You don't need a separate `enable` after
  `preset` — but you do after a manual `set` only if your config's `enabled` is
  false.
