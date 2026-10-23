package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func TestModifyResponse_GzipCompression(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	htmlContent := []byte("<html><head><title>Test</title></head><body>Hello World</body></html>")

	// Compress the HTML content
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	_, err := gzWriter.Write(htmlContent)
	if err != nil {
		t.Fatalf("failed to compress: %v", err)
	}
	gzWriter.Close()

	// Create a response with gzip-compressed content
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(&buf),
	}

	// Modify the response
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err = ps.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse failed: %v", err)
	}

	// Read the modified response
	modifiedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}

	// Verify it's no longer compressed (Content-Encoding should be removed)
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Content-Encoding header should be removed, got: %s", resp.Header.Get("Content-Encoding"))
	}

	// Verify the content is valid HTML (not binary garbage)
	bodyStr := string(modifiedBody)
	if !strings.Contains(bodyStr, "<html>") {
		t.Errorf("modified body doesn't contain <html> tag")
	}
	if !strings.Contains(bodyStr, "Hello World") {
		t.Errorf("modified body doesn't contain original content")
	}

	// Verify instrumentation was injected. The bundle is served externally now,
	// so the decompressed body carries the external <script src> asset tag
	// rather than the inlined bundle code.
	if !strings.Contains(bodyStr, "__devtool") {
		t.Errorf("instrumentation script was not injected")
	}
	if !strings.Contains(bodyStr, instrumentationAssetPrefix) {
		t.Errorf("external instrumentation asset tag was not injected")
	}
}

func TestModifyResponse_DeflateCompression(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	htmlContent := []byte("<html><head><title>Test</title></head><body>Hello World</body></html>")

	// Compress the HTML content with deflate
	var buf bytes.Buffer
	flateWriter, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("failed to create deflate writer: %v", err)
	}
	_, err = flateWriter.Write(htmlContent)
	if err != nil {
		t.Fatalf("failed to compress: %v", err)
	}
	flateWriter.Close()

	// Create a response with deflate-compressed content
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"deflate"},
		},
		Body: io.NopCloser(&buf),
	}

	// Modify the response
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err = ps.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse failed: %v", err)
	}

	// Read the modified response
	modifiedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}

	// Verify it's no longer compressed
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Content-Encoding header should be removed, got: %s", resp.Header.Get("Content-Encoding"))
	}

	// Verify the content is valid HTML
	bodyStr := string(modifiedBody)
	if !strings.Contains(bodyStr, "<html>") {
		t.Errorf("modified body doesn't contain <html> tag")
	}
	if !strings.Contains(bodyStr, "Hello World") {
		t.Errorf("modified body doesn't contain original content")
	}

	// Verify instrumentation was injected
	if !strings.Contains(bodyStr, "__devtool") {
		t.Errorf("instrumentation script was not injected")
	}
}

// TestModifyResponse_DeflateZlibWrapped: RFC 9110 defines Content-Encoding
// "deflate" as the zlib format (RFC 1950), which most servers send. Decoding it
// as raw DEFLATE fails mid-read and the page became a 502.
func TestModifyResponse_DeflateZlibWrapped(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080"}
	htmlContent := []byte("<html><head><title>Test</title></head><body>Hello Zlib</body></html>")

	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(htmlContent); err != nil {
		t.Fatalf("failed to compress: %v", err)
	}
	zw.Close()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"deflate"},
		},
		Body: io.NopCloser(&buf),
	}
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse failed on zlib-wrapped deflate (would be a 502): %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Content-Encoding should be removed, got %q", resp.Header.Get("Content-Encoding"))
	}
	if !strings.Contains(string(body), "Hello Zlib") || !strings.Contains(string(body), "__devtool") {
		t.Errorf("body not decoded and injected: %q", body)
	}
}

func TestModifyResponse_NoCompression(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	htmlContent := []byte("<html><head><title>Test</title></head><body>Hello World</body></html>")

	// Create a response with uncompressed content
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/html"},
		},
		Body: io.NopCloser(bytes.NewReader(htmlContent)),
	}

	// Modify the response
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err := ps.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse failed: %v", err)
	}

	// Read the modified response
	modifiedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}

	// Verify the content is valid HTML
	bodyStr := string(modifiedBody)
	if !strings.Contains(bodyStr, "<html>") {
		t.Errorf("modified body doesn't contain <html> tag")
	}
	if !strings.Contains(bodyStr, "Hello World") {
		t.Errorf("modified body doesn't contain original content")
	}

	// Verify instrumentation was injected
	if !strings.Contains(bodyStr, "__devtool") {
		t.Errorf("instrumentation script was not injected")
	}
}

func TestModifyResponse_NonHTMLSkipped(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	jsonContent := []byte(`{"message": "Hello World"}`)

	// Create a response with JSON content (should not be modified)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(bytes.NewReader(jsonContent)),
	}

	// Modify the response
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err := ps.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse failed: %v", err)
	}

	// Read the response
	modifiedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}

	// Verify JSON content is unchanged
	if !bytes.Equal(modifiedBody, jsonContent) {
		t.Errorf("JSON content should be unchanged, got: %s", string(modifiedBody))
	}

	// Verify instrumentation was NOT injected
	if strings.Contains(string(modifiedBody), "__devtool") {
		t.Errorf("instrumentation should not be injected in non-HTML responses")
	}
}

func TestModifyResponse_CorruptGzipData(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	// Create corrupt gzip data
	corruptData := []byte("this is not valid gzip data")

	// Create a response with corrupt gzip content
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"gzip"},
		},
		Body: io.NopCloser(bytes.NewReader(corruptData)),
	}

	// Modify the response (should not return error, just skip injection)
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err := ps.modifyResponse(resp)
	if err != nil {
		t.Errorf("modifyResponse should handle corrupt gzip gracefully, got error: %v", err)
	}
}

// The ModifyResponse benchmarks share one fixture (benchHTML), one proxy
// configuration and one drain pattern with BenchmarkModifyResponse_Identity, and
// run at three payload sizes so encodings are comparable with each other and
// across sizes. The helpers live in rewrite_bench_test.go.

func BenchmarkModifyResponse_Gzip(b *testing.B) { benchModifyEncoded(b, "gzip", encodeGzip) }

func BenchmarkModifyResponse_NoCompression(b *testing.B) { benchModifyEncoded(b, "", encodeIdentity) }

func TestModifyResponse_BrotliCompression(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	htmlContent := []byte("<html><head><title>Test</title></head><body>Hello World</body></html>")

	// Compress the HTML content with Brotli
	var buf bytes.Buffer
	brWriter := brotli.NewWriter(&buf)
	_, err := brWriter.Write(htmlContent)
	if err != nil {
		t.Fatalf("failed to compress: %v", err)
	}
	brWriter.Close()

	// Create a response with brotli-compressed content
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"br"},
		},
		Body: io.NopCloser(&buf),
	}

	// Modify the response
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err = ps.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse failed: %v", err)
	}

	// Read the modified response
	modifiedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}

	// Verify it's no longer compressed
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Content-Encoding header should be removed, got: %s", resp.Header.Get("Content-Encoding"))
	}

	// Verify the content is valid HTML
	bodyStr := string(modifiedBody)
	if !strings.Contains(bodyStr, "<html>") {
		t.Errorf("modified body doesn't contain <html> tag")
	}
	if !strings.Contains(bodyStr, "Hello World") {
		t.Errorf("modified body doesn't contain original content")
	}

	// Verify instrumentation was injected
	if !strings.Contains(bodyStr, "__devtool") {
		t.Errorf("instrumentation script was not injected")
	}
}

func TestModifyResponse_ZstdCompression(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	htmlContent := []byte("<html><head><title>Test</title></head><body>Hello World</body></html>")

	// Compress the HTML content with Zstandard
	var buf bytes.Buffer
	zstdWriter, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("failed to create zstd writer: %v", err)
	}
	_, err = zstdWriter.Write(htmlContent)
	if err != nil {
		t.Fatalf("failed to compress: %v", err)
	}
	zstdWriter.Close()

	// Create a response with zstd-compressed content
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"zstd"},
		},
		Body: io.NopCloser(&buf),
	}

	// Modify the response
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err = ps.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse failed: %v", err)
	}

	// Read the modified response
	modifiedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read modified body: %v", err)
	}

	// Verify it's no longer compressed
	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Content-Encoding header should be removed, got: %s", resp.Header.Get("Content-Encoding"))
	}

	// Verify the content is valid HTML
	bodyStr := string(modifiedBody)
	if !strings.Contains(bodyStr, "<html>") {
		t.Errorf("modified body doesn't contain <html> tag")
	}
	if !strings.Contains(bodyStr, "Hello World") {
		t.Errorf("modified body doesn't contain original content")
	}

	// Verify instrumentation was injected
	if !strings.Contains(bodyStr, "__devtool") {
		t.Errorf("instrumentation script was not injected")
	}
}

func TestModifyResponse_UnsupportedCompression(t *testing.T) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	// Create a response with an unsupported compression format
	htmlContent := []byte("<html><head><title>Test</title></head><body>Hello World</body></html>")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"text/html"},
			"Content-Encoding": []string{"lzma"},
		},
		Body: io.NopCloser(bytes.NewReader(htmlContent)),
	}

	// Modify the response (should pass through without modification)
	t.Cleanup(func() { resp.Body.Close() }) // releases the injection-budget reservation
	err := ps.modifyResponse(resp)
	if err != nil {
		t.Fatalf("modifyResponse should handle unsupported encoding gracefully, got error: %v", err)
	}

	// Read the response
	modifiedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	// Verify content is unchanged (no decompression attempted)
	if !bytes.Equal(modifiedBody, htmlContent) {
		t.Errorf("body should be unchanged for unsupported encoding")
	}

	// Verify instrumentation was NOT injected (since we can't decode)
	if strings.Contains(string(modifiedBody), "__devtool") {
		t.Errorf("instrumentation should not be injected for unsupported encoding")
	}
}

func BenchmarkModifyResponse_Brotli(b *testing.B) { benchModifyEncoded(b, "br", encodeBrotli) }

func BenchmarkModifyResponse_Zstd(b *testing.B) { benchModifyEncoded(b, "zstd", encodeZstd) }
