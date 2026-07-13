package sshclient

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

const windowsPipeOwnerOnlySDDL = "D:P(A;;GA;;;OW)"

func windowsPipeName(kind, host string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(host))
	if normalized == "" {
		return "", fmt.Errorf("sshclient: empty host has no %s pipe", kind)
	}
	var safe strings.Builder
	for _, r := range normalized {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			safe.WriteRune(r)
		} else {
			safe.WriteByte('-')
		}
	}
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf(`\\.\pipe\agnt-ssh-%s-%s-%x`, kind, safe.String(), sum[:6]), nil
}

func validateWindowsPipePath(path string) error {
	if !strings.HasPrefix(strings.ToLower(path), `\\.\pipe\`) {
		return fmt.Errorf("sshclient: native Windows endpoint must be a named pipe, got %q", path)
	}
	return nil
}
