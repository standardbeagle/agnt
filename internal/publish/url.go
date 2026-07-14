package publish

import (
	"net/url"
	"strings"
)

// ValidateURL enforces spec §5: https-only, length-capped, no control chars.
// Rejects http/data/javascript/file/blob and anything unparseable. Public
// webapp ⇒ TLS mandatory; blocks JS/data URL injection.
func ValidateURL(raw string) error {
	if raw == "" {
		return errf("url: empty")
	}
	if len(raw) > MaxURLLength {
		return errf("url: length %d exceeds max %d", len(raw), MaxURLLength)
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return errf("url: contains control character")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errf("url: unparseable: %w", err)
	}
	if strings.ToLower(u.Scheme) != "https" {
		return errf("url: scheme %q not allowed (https only)", u.Scheme)
	}
	if u.Host == "" {
		return errf("url: missing host")
	}
	return nil
}
