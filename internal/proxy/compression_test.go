package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
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

	// Verify instrumentation was injected
	if !strings.Contains(bodyStr, "__devtool") {
		t.Errorf("instrumentation script was not injected")
	}
	if !strings.Contains(bodyStr, "WebSocket") {
		t.Errorf("WebSocket code was not injected")
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
	err := ps.modifyResponse(resp)
	if err != nil {
		t.Errorf("modifyResponse should handle corrupt gzip gracefully, got error: %v", err)
	}
}

// Benchmark decompression overhead
func BenchmarkModifyResponse_Gzip(b *testing.B) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	htmlContent := []byte("<html><head><title>Test</title></head><body>" + strings.Repeat("Hello World ", 1000) + "</body></html>")

	// Pre-compress content
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	gzWriter.Write(htmlContent)
	gzWriter.Close()
	compressedData := buf.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type":     []string{"text/html"},
				"Content-Encoding": []string{"gzip"},
			},
			Body: io.NopCloser(bytes.NewReader(compressedData)),
		}
		ps.modifyResponse(resp)
		io.ReadAll(resp.Body)
	}
}

func BenchmarkModifyResponse_NoCompression(b *testing.B) {
	ps := &ProxyServer{
		ListenAddr: ":8080",
	}

	htmlContent := []byte("<html><head><title>Test</title></head><body>" + strings.Repeat("Hello World ", 1000) + "</body></html>")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/html"},
			},
			Body: io.NopCloser(bytes.NewReader(htmlContent)),
		}
		ps.modifyResponse(resp)
		io.ReadAll(resp.Body)
	}
}
