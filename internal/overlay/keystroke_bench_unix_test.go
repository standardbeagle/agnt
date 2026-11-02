//go:build unix

package overlay

import (
	"io"
	"os"
	"testing"
)

// One keystroke round trip: a byte written to the input pipe comes out of
// the agent's side of the PTY. The chain is the production input path (input
// router -> AgentTTY -> PTY master). The floor is one goroutine copying the
// input pipe to the same PTY master: the read and write syscalls a relay
// cannot avoid.

func keystrokeLoop(b *testing.B, in *os.File, slave *os.File) {
	key, got := []byte{'a'}, make([]byte, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := in.Write(key); err != nil {
			b.Fatal(err)
		}
		if _, err := slave.Read(got); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKeystroke_Router(b *testing.B) {
	master, slave := pollablePTY(b)
	pr, pw, err := os.Pipe()
	if err != nil {
		b.Fatal(err)
	}
	defer pw.Close()
	cfg := DefaultConfig()
	cfg.ShowIndicator = false
	ov := New(nil, 80, 24, cfg)
	tty := NewAgentTTY(master)
	r := NewInputRouter(tty, ov)
	r.SetInput(pr)
	go func() { _ = r.Run() }()
	defer r.Stop()
	keystrokeLoop(b, pw, slave)
}

func BenchmarkKeystroke_Floor(b *testing.B) {
	master, slave := pollablePTY(b)
	pr, pw, err := os.Pipe()
	if err != nil {
		b.Fatal(err)
	}
	defer pw.Close()
	go func() { _, _ = io.Copy(master, pr) }()
	keystrokeLoop(b, pw, slave)
}
