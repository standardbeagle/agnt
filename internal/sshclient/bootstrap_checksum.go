package sshclient

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxChecksumsSize bounds the SHA256SUMS fetch; the published file lists a
// few dozen assets and is a few KiB.
const maxChecksumsSize = 64 << 10

// fetchReleaseChecksums GETs a release's SHA256SUMS asset. Anything but a
// complete 200 response is an error, so a release that publishes no checksums
// (a 404) refuses the install rather than skipping verification.
func fetchReleaseChecksums(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s (the release publishes no SHA256SUMS)", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumsSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxChecksumsSize {
		return nil, fmt.Errorf("SHA256SUMS is larger than %d bytes", maxChecksumsSize)
	}
	return body, nil
}

// checksumFor returns the lowercase hex SHA-256 that sums (sha256sum output:
// "<hex>  <name>", or "<hex> *<name>" in binary mode) lists for name.
func checksumFor(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if b, err := hex.DecodeString(sum); err != nil || len(b) != 32 {
			return "", fmt.Errorf("SHA256SUMS entry for %s is not a SHA-256 digest: %q", name, fields[0])
		}
		return sum, nil
	}
	return "", fmt.Errorf("SHA256SUMS lists no %s", name)
}
