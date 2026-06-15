// Package replaytest implements a record→worker-mock→replay pipeline for
// deterministic, fully-local front-end testing.
//
// The pipeline captures live proxy traffic into a scenario, serves the
// recorded API responses back from an in-page web worker during replay, and
// drives a headless browser against the mocked app so automation runs without
// touching any real backend. Fuzz mutators perturb the recorded responses to
// probe front-end resilience, and a breadth-exploration step partitions seeds
// for fan-out across browser-debugger subagents.
//
// Units:
//   - scenario:      on-disk scenario model (.agnt/replaytests/<name>.json)
//   - match:         request matching of live calls against recorded entries
//   - fuzz:          response mutators (empty_array, http_error, ...)
//   - domsig:        DOM signature capture/diff for pass/fail assertions
//   - worker_bundle: the in-page web worker that serves recorded responses
//   - recorder:      assembles scenarios from captured proxy traffic
//   - report:        scenario run report model (<name>.report.json)
//   - driver:        headless-chrome replay driver with in-page network mock
//   - refine:        LLM-assisted scenario cleanup (needs an API key)
//   - explore:       seed partitioning for subagent breadth exploration
//   - store:         scenario/report persistence
//
// All record/refine/replay/explore actions are license-gated behind
// license.CapAdvancedTesting (Pro: advanced_testing).
package replaytest
