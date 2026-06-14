package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/standardbeagle/agnt/internal/debug"
)

// modifyResponse rewrites URLs and injects JavaScript into HTML responses.
func (ps *ProxyServer) modifyResponse(resp *http.Response) error {
	// Rewrite Location header for redirects
	ps.rewriteLocationHeader(resp)

	// Rewrite Set-Cookie headers for domain/path
	ps.rewriteSetCookieHeaders(resp)

	contentType := resp.Header.Get("Content-Type")
	if !ShouldInject(contentType) {
		return nil
	}

	// Check if response is compressed
	encoding := strings.ToLower(resp.Header.Get("Content-Encoding"))
	var bodyReader io.ReadCloser = resp.Body

	// Decompress if needed
	if strings.Contains(encoding, "gzip") {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			// If decompression fails, skip injection and pass through original
			debug.Log("proxy", "Failed to decompress gzip response: %v", err)
			return nil
		}
		defer gzReader.Close()
		bodyReader = gzReader
	} else if strings.Contains(encoding, "deflate") {
		deflateReader := flate.NewReader(resp.Body)
		defer deflateReader.Close()
		bodyReader = deflateReader
	} else if strings.Contains(encoding, "br") {
		brReader := brotli.NewReader(resp.Body)
		bodyReader = io.NopCloser(brReader)
		defer bodyReader.Close()
	} else if strings.Contains(encoding, "zstd") {
		zstdReader, err := zstd.NewReader(resp.Body)
		if err != nil {
			// If decompression fails, skip injection and pass through original
			debug.Log("proxy", "Failed to decompress zstd response: %v", err)
			return nil
		}
		defer zstdReader.Close()
		bodyReader = io.NopCloser(zstdReader)
	} else if encoding != "" && encoding != "identity" {
		// Unsupported encoding - pass through without modification
		debug.Log("proxy", "Unsupported Content-Encoding: %s - passing through without injection", encoding)
		return nil
	}

	// Read the body. For identity (non-decompressed) responses the origin's
	// Content-Length is the exact size, so presize the buffer to a single
	// allocation and avoid io.ReadAll's grow-by-doubling memmove storm (the
	// dominant cost on large HTML bodies). For decompressed streams the
	// decompressed size is unknown, so fall back to io.ReadAll.
	var sizeHint int64 = -1
	if bodyReader == resp.Body {
		sizeHint = resp.ContentLength
	}
	bodyBytes, err := readAllSized(bodyReader, sizeHint)
	if err != nil {
		return err
	}
	resp.Body.Close()

	// Extract port from ListenAddr (handles both :port and [::]:port formats)
	port := 8080
	if lastColon := strings.LastIndex(ps.ListenAddr, ":"); lastColon != -1 {
		if p, err := strconv.Atoi(ps.ListenAddr[lastColon+1:]); err == nil {
			port = p
		}
	}

	// Rewrite absolute URLs in HTML content pointing to target back to proxy
	modifiedBody := ps.rewriteURLsInBody(bodyBytes)

	// Inject instrumentation + proxy-id meta in a single full-body copy.
	_ = port // wsPort is deprecated/unused; kept for signature compatibility
	modifiedBody = InjectInstrumentationAndMeta(modifiedBody, ps.ID)

	// Update response with uncompressed modified content
	resp.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	resp.ContentLength = int64(len(modifiedBody))
	resp.Header.Set("Content-Length", strconv.Itoa(len(modifiedBody)))

	// Remove encoding headers since we're returning uncompressed content
	resp.Header.Del("Content-Encoding")

	return nil
}

// readAllSized reads all of r into a single buffer. When sizeHint > 0 it
// presizes the buffer to that exact size plus a small read margin, so a body
// whose length is known up front (identity Content-Length) is read in one
// allocation with no grow-by-doubling reallocations. A non-positive hint
// (unknown length — chunked or decompressed) falls back to io.ReadAll.
func readAllSized(r io.Reader, sizeHint int64) ([]byte, error) {
	if sizeHint <= 0 {
		return io.ReadAll(r)
	}
	buf := bytes.NewBuffer(make([]byte, 0, sizeHint+bytes.MinRead))
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

// rewriteLocationHeader rewrites Location headers to point to the proxy instead of the target.
func (ps *ProxyServer) rewriteLocationHeader(resp *http.Response) {
	location := resp.Header.Get("Location")
	if location == "" {
		return
	}

	rewritten := ps.rewriteURL(location)
	if rewritten != location {
		resp.Header.Set("Location", rewritten)
	}
}

// rewriteSetCookieHeaders rewrites Set-Cookie headers to work with the proxy domain.
func (ps *ProxyServer) rewriteSetCookieHeaders(resp *http.Response) {
	cookies := resp.Header["Set-Cookie"]
	if len(cookies) == 0 {
		return
	}

	targetHost := ps.TargetURL.Hostname()

	for i, cookie := range cookies {
		// Remove or rewrite Domain attribute if it matches target
		// This allows cookies to work on localhost proxy
		if strings.Contains(strings.ToLower(cookie), "domain=") {
			// Parse and rebuild cookie without domain restriction
			// or with proxy domain
			cookies[i] = ps.rewriteCookieDomain(cookie, targetHost)
		}
	}

	resp.Header["Set-Cookie"] = cookies
}

// rewriteCookieDomain removes or rewrites the Domain attribute in a Set-Cookie header.
func (ps *ProxyServer) rewriteCookieDomain(cookie string, targetHost string) string {
	// Split cookie into parts
	parts := strings.Split(cookie, ";")
	var newParts []string

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		lower := strings.ToLower(trimmed)

		// Skip domain attributes that match target host
		if strings.HasPrefix(lower, "domain=") {
			domainValue := strings.TrimPrefix(lower, "domain=")
			domainValue = strings.TrimPrefix(domainValue, ".") // Remove leading dot

			// If domain matches target, remove it entirely (allows cookie on any domain)
			if strings.Contains(targetHost, domainValue) || strings.Contains(domainValue, targetHost) {
				continue
			}
		}

		newParts = append(newParts, part)
	}

	return strings.Join(newParts, ";")
}

// rewriteURL rewrites a URL from the target server to the proxy server.
func (ps *ProxyServer) rewriteURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// Only rewrite absolute URLs that point to the target
	if parsed.Host == "" {
		// Relative URL, no rewriting needed
		return rawURL
	}

	// Check if this URL points to our target
	targetHost := ps.TargetURL.Host
	if parsed.Host != targetHost {
		// Different host, don't rewrite
		return rawURL
	}

	// Rewrite to proxy URL
	// Extract proxy host from ListenAddr
	proxyHost := ps.getProxyHost()
	proxyScheme := ps.getProxyScheme()

	parsed.Scheme = proxyScheme
	parsed.Host = proxyHost

	return parsed.String()
}

// getProxyHost returns the host:port for the proxy server.
// If a public URL is configured (for tunnels), returns that host.
// Otherwise returns localhost:port for local development.
func (ps *ProxyServer) getProxyHost() string {
	// If a public URL is configured (for tunnels), use its host
	if ps.PublicURL != "" {
		if parsed, err := url.Parse(ps.PublicURL); err == nil && parsed.Host != "" {
			return parsed.Host
		}
	}

	// ListenAddr is in format "addr:port" or "[::]:port"
	// We need to return "localhost:port" for redirect purposes
	port := "8080"
	if lastColon := strings.LastIndex(ps.ListenAddr, ":"); lastColon != -1 {
		port = ps.ListenAddr[lastColon+1:]
	}
	return "localhost:" + port
}

// getProxyScheme returns the scheme (http/https) for the proxy server.
// If a public URL is configured with HTTPS (common for tunnels), returns https.
func (ps *ProxyServer) getProxyScheme() string {
	if ps.PublicURL != "" {
		if parsed, err := url.Parse(ps.PublicURL); err == nil && parsed.Scheme != "" {
			return parsed.Scheme
		}
	}
	return "http"
}

// rewriteURLsInBody rewrites absolute URLs in HTML/JS content from target to proxy.
func (ps *ProxyServer) rewriteURLsInBody(body []byte) []byte {
	// Guard against nil TargetURL (can happen in tests with partial setup)
	if ps.TargetURL == nil {
		return body
	}

	targetHost := ps.TargetURL.Host
	if targetHost == "" {
		return body
	}

	proxyHost := ps.getProxyHost()
	proxyScheme := ps.getProxyScheme()

	// Rewrite common URL patterns pointing to target
	// http://target:port -> scheme://proxyhost
	// https://target:port -> scheme://proxyhost

	// Build replacement patterns
	targetHTTP := "http://" + targetHost
	targetHTTPS := "https://" + targetHost
	proxyURL := proxyScheme + "://" + proxyHost

	// Also handle URLs with escaped slashes (common in JSON)
	targetHTTPEscaped := strings.ReplaceAll(targetHTTP, "/", "\\/")
	targetHTTPSEscaped := strings.ReplaceAll(targetHTTPS, "/", "\\/")
	proxyURLEscaped := strings.ReplaceAll(proxyURL, "/", "\\/")

	// Replace URLs (simple byte replacement for performance). bytes.ReplaceAll
	// allocates a full-body copy even when there is no match, so each pattern
	// is guarded by a cheap Contains scan: a body with no absolute target URLs
	// (the common case for dev servers serving relative paths) passes through
	// with zero copies instead of four.
	result := replaceIfPresent(body, targetHTTPS, proxyURL)
	result = replaceIfPresent(result, targetHTTP, proxyURL)
	result = replaceIfPresent(result, targetHTTPSEscaped, proxyURLEscaped)
	result = replaceIfPresent(result, targetHTTPEscaped, proxyURLEscaped)

	return result
}

// replaceIfPresent replaces all occurrences of old with new in body, but only
// allocates a new buffer when old is actually present. When old is absent it
// returns body unchanged (bytes.ReplaceAll would otherwise copy the whole
// buffer regardless). old/new are strings to avoid caller-side []byte churn.
func replaceIfPresent(body []byte, old, new string) []byte {
	if old == "" || !bytes.Contains(body, []byte(old)) {
		return body
	}
	return bytes.ReplaceAll(body, []byte(old), []byte(new))
}
