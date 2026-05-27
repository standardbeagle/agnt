package platform

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// TailscaleDNSName returns this node's MagicDNS name (no trailing dot),
// e.g. "machine.tailnet.ts.net". Returns "" if tailscale is unavailable,
// not logged in, or the lookup times out.
//
// If ctx has no deadline, a 2s timeout is applied. Safe to call without
// tailscale installed — exec errors collapse to "".
func TailscaleDNSName(ctx context.Context) string {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	return parseTailscaleDNSName(output)
}

// parseTailscaleDNSName extracts Self.DNSName from `tailscale status --json`
// output and trims any trailing dot. Returns "" on parse failure.
//
// Split out for testability without spawning the binary.
func parseTailscaleDNSName(output []byte) string {
	var status struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return ""
	}
	return strings.TrimSuffix(status.Self.DNSName, ".")
}
