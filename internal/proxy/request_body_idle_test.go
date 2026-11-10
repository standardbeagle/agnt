package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStreamedRequestBody_StalledUploadIsCutOff pins the dev proxy's body
// read bound. The listener streams (httpcaps.Streaming: no whole-request read
// deadline), and only a small known-length body got a capture deadline, so a
// chunked upload that sends one chunk and then nothing held its handler, its
// upstream request and one of the 256 connection slots forever. A streamed
// body now gets an idle deadline: each read must make progress within
// requestBodyIdleTimeout.
func TestStreamedRequestBody_StalledUploadIsCutOff(t *testing.T) {
	prev := requestBodyIdleTimeout
	requestBodyIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { requestBodyIdleTimeout = prev })

	backendErr := make(chan error, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		backendErr <- err
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{ID: "body-idle-test", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 50})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { ps.Stop(context.Background()) })
	<-ps.Ready()

	conn, err := net.Dial("tcp", ps.liveAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "POST /upload HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nContent-Type: application/octet-stream\r\n\r\n5\r\nhello\r\n", ps.liveAddr())

	select {
	case err := <-backendErr:
		if err == nil {
			t.Fatal("backend read a complete body from a stalled upload")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled chunked upload held the request past the idle bound")
	}
	// The client is answered or dropped, not left hanging.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := bufio.NewReader(conn).ReadByte(); err != nil {
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("client connection still held open after the stall was cut off")
		}
	}
}

// TestStreamedRequestBody_SteadyUploadIsNotCutOff: the bound is on idleness,
// not on the whole upload, so a slow but steady body that takes longer than
// the idle timeout in total still arrives whole.
func TestStreamedRequestBody_SteadyUploadIsNotCutOff(t *testing.T) {
	prev := requestBodyIdleTimeout
	requestBodyIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { requestBodyIdleTimeout = prev })

	got := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		got <- string(b)
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{ID: "body-idle-steady", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 50})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { ps.Stop(context.Background()) })
	<-ps.Ready()

	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 6; i++ {
			fmt.Fprintf(pw, "part%d;", i)
			time.Sleep(100 * time.Millisecond)
		}
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, "http://"+ps.liveAddr()+"/upload", pr)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if body := <-got; body != "part0;part1;part2;part3;part4;part5;" {
		t.Fatalf("backend got %q", body)
	}
}
