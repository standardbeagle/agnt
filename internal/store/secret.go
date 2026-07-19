package store

import (
	"fmt"
	"regexp"
)

// MetaSecret is the metadata key that marks a store entry as a secret.
// Secret entries are write-only through every agent-visible read surface:
// STORE GET / GET-ALL return a masked ref (name + fingerprint), never the
// value. The only consumption path is env injection at process spawn.
//
// At-rest storage note: secret entries are persisted to the global store
// file on disk in PLAINTEXT, exactly like any other store entry — this
// package provides no at-rest encryption. That is inherent to the by-name
// env-injection design (injectSecretEnv, internal/daemon/secret_entry.go,
// reads the plaintext value back out to build a child process's env), not
// an oversight. The guarantee this feature actually makes is narrower and
// different: the secret value is kept out of the LLM/channel event stream
// (traffic log, overlay notifications, browser-bound WS frames) — see
// TestSecretSubmit_E2E_ValueNeverEntersTranscript in
// internal/proxy/secret_entry_test.go. Anyone with filesystem read access to
// the store file can read secrets in the clear; that is a separate threat
// model from the zero-leak-to-agent invariant this package enforces.
const MetaSecret = "secret"

// MetaFingerprint is the metadata key holding the display fingerprint
// (last 4 characters) recorded when a secret is stored.
const MetaFingerprint = "fingerprint"

// envNameRe matches POSIX-portable environment variable names. Secrets are
// consumed by NAME via env injection, so the key must be env-safe.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidSecretName reports whether name is a valid env-var-style secret name.
func ValidSecretName(name string) bool {
	return envNameRe.MatchString(name)
}

// Fingerprint returns the displayable last-4 fingerprint of a secret value.
// Values shorter than 8 characters return "" — revealing half of a short
// secret is worse than revealing nothing.
func Fingerprint(value string) string {
	if len(value) < 8 {
		return ""
	}
	return value[len(value)-4:]
}

// IsSecretEntry reports whether the entry carries the secret metadata marker.
func IsSecretEntry(e *StoreEntry) bool {
	if e == nil || e.Metadata == nil {
		return false
	}
	b, ok := e.Metadata[MetaSecret].(bool)
	return ok && b
}

// MaskedRef returns the agent-visible reference for a secret entry:
// the name plus the stored fingerprint, never the value.
func MaskedRef(key string, e *StoreEntry) string {
	fp := ""
	if e != nil && e.Metadata != nil {
		fp, _ = e.Metadata[MetaFingerprint].(string)
	}
	return fmt.Sprintf("[secret:%s ****%s]", key, fp)
}

// MaskedForRead returns the entry to expose on a read surface. Non-secret
// entries pass through unchanged. Secret entries are cloned with the value
// replaced by the masked ref — the original entry is never mutated (it may
// be a pointer into the loaded store file).
func MaskedForRead(key string, e *StoreEntry) *StoreEntry {
	if !IsSecretEntry(e) {
		return e
	}
	masked := *e
	masked.Value = MaskedRef(key, e)
	masked.Type = TypeString
	return &masked
}
