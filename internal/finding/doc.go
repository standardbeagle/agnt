// Package finding defines the shared finding contract that incidents and
// audits project into. It is a leaf package: it imports only the standard
// library so every higher layer (incident pipeline, audit tools,
// verify_change) can depend on it without an import cycle.
//
// A Finding is a single actionable observation. Producer carries the exact
// tool call (tool name + args) that regenerates the finding; it is the input
// verify_change uses to re-run the producing check and confirm the finding
// is gone.
//
// StableID reproduces the FNV-1a 32-bit hash already implemented in
// audit-report.js (generateFindingId) and pinned by
// internal/proxy/scripts/audit_ids_test.go, with a kind prefix so incident
// fingerprints and audit IDs can be distinguished without collision.
package finding
