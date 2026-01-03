// Package store provides persistent key-value storage with three scopes:
// global (project-wide), folder (URL path prefix), and page (specific URL).
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// NormalizeURL removes query params, hash fragments, and trailing slashes.
// This creates a canonical URL form for consistent storage keys.
func NormalizeURL(rawURL string) string {
	// Parse the URL
	u, err := url.Parse(rawURL)
	if err != nil {
		// If parsing fails, just clean up what we can
		return cleanURLString(rawURL)
	}

	// Remove query and fragment
	u.RawQuery = ""
	u.Fragment = ""

	// Get the clean URL string
	normalized := u.String()

	// Remove trailing slashes (but keep root "/")
	// Special handling for scheme://host URLs
	if u.Path == "" || u.Path == "/" {
		// For URLs like "https://example.com" or "https://example.com/"
		// ensure they end with "/"
		if !strings.HasSuffix(normalized, "/") {
			normalized += "/"
		}
	} else {
		// For URLs with paths, remove trailing slashes
		normalized = strings.TrimRight(normalized, "/")
	}

	return normalized
}

// cleanURLString performs basic cleanup on a URL string.
func cleanURLString(rawURL string) string {
	// Remove query params
	if idx := strings.Index(rawURL, "?"); idx != -1 {
		rawURL = rawURL[:idx]
	}

	// Remove hash
	if idx := strings.Index(rawURL, "#"); idx != -1 {
		rawURL = rawURL[:idx]
	}

	// Remove trailing slashes (but keep root "/")
	rawURL = strings.TrimRight(rawURL, "/")
	if rawURL == "" {
		rawURL = "/"
	}

	return rawURL
}

// GetFolderKey extracts the folder/path prefix from a URL.
// For example:
//   - "/products/123" -> "/products/"
//   - "/products/" -> "/products/"
//   - "/products" -> "/products/"
//   - "/" -> "/"
//   - "https://example.com/api/users/42" -> "/api/users/"
func GetFolderKey(rawURL string) string {
	// Check if the original URL ended with a slash before normalization
	originalEndsWithSlash := strings.HasSuffix(rawURL, "/")

	// Normalize the URL
	normalized := NormalizeURL(rawURL)

	// Parse to get just the path
	u, err := url.Parse(normalized)
	if err != nil {
		// Fallback: extract path from string
		return extractPathFolder(normalized)
	}

	path := u.Path
	if path == "" || path == "/" {
		return "/"
	}

	// If the original URL ended with a slash, treat the whole path as a folder
	if originalEndsWithSlash {
		// Ensure it ends with a slash
		if !strings.HasSuffix(path, "/") {
			return path + "/"
		}
		return path
	}

	// Split into segments
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 {
		return "/"
	}

	// Remove last segment (assumed to be a specific resource)
	if len(segments) == 1 {
		// Just one segment, use root
		return "/"
	}

	// Build folder path from all but last segment
	folderPath := "/" + strings.Join(segments[:len(segments)-1], "/") + "/"
	return folderPath
}

// extractPathFolder extracts the folder portion from a path string.
func extractPathFolder(pathStr string) string {
	// Find the path part (after protocol and domain)
	pathStart := strings.Index(pathStr, "://")
	if pathStart != -1 {
		pathStart = strings.Index(pathStr[pathStart+3:], "/")
		if pathStart == -1 {
			return "/"
		}
		pathStr = pathStr[pathStart:]
	}

	if pathStr == "" || pathStr == "/" {
		return "/"
	}

	segments := strings.Split(strings.Trim(pathStr, "/"), "/")
	if len(segments) <= 1 {
		return "/"
	}

	return "/" + strings.Join(segments[:len(segments)-1], "/") + "/"
}

// HashScopeKey generates a safe filename hash from a scope key.
// Uses SHA256 and returns the first 16 characters of the hex digest.
// This ensures consistent, filesystem-safe names for scope files.
func HashScopeKey(scopeKey string) string {
	if scopeKey == "" {
		return "global"
	}

	hash := sha256.Sum256([]byte(scopeKey))
	hexStr := hex.EncodeToString(hash[:])

	// Take first 16 characters for a reasonably short filename
	// while maintaining very low collision probability
	return hexStr[:16]
}
