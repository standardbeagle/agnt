package overlay

import (
	"math/rand"
	"testing"
)

// The recorder's ring matches a reference model at every step: the newest
// events whose bytes fit the budget, oldest first, with an oversized event
// cut to its newest budget bytes. Many small events drive the drop index
// through repeated compactions.
func TestTTYRecorderMatchesModelAcrossCompactions(t *testing.T) {
	const budget = 64
	rng := rand.New(rand.NewSource(1))
	r := NewTTYRecorder(budget)
	var model [][]byte
	for step := 0; step < 5000; step++ {
		n := 1 + rng.Intn(9)
		if step%997 == 0 {
			n = budget + 5 // oversized
		}
		data := make([]byte, n)
		for i := range data {
			data[i] = byte('a' + (step+i)%26)
		}
		r.Record(TTYIn, "k", data)

		if len(data) > budget {
			data = data[len(data)-budget:]
		}
		model = append(model, data)
		total := 0
		for _, d := range model {
			total += len(d)
		}
		for total > budget {
			total -= len(model[0])
			model = model[1:]
		}

		got := r.Events()
		if len(got) != len(model) {
			t.Fatalf("step %d: %d events, model has %d", step, len(got), len(model))
		}
		for i := range got {
			if string(got[i].Data) != string(model[i]) {
				t.Fatalf("step %d event %d: %q, model %q", step, i, got[i].Data, model[i])
			}
		}
		if r.in.bytes != total {
			t.Fatalf("step %d: byte count %d, model %d", step, r.in.bytes, total)
		}
		if dead := r.in.start; dead > len(r.in.events)-dead {
			t.Fatalf("step %d: %d dropped slots kept for %d live; compaction missed", step, dead, len(r.in.events)-dead)
		}
	}
}

// BenchmarkTTYRecorder_KeystrokeFullRing records one-byte events (a typed key
// or its echo) into a recorder that is already at its byte budget, the steady
// state of any session longer than a few minutes. Floor: one time.Now, one
// lock, one small copy, about 100 ns.
func BenchmarkTTYRecorder_KeystrokeFullRing(b *testing.B) {
	r := NewTTYRecorder(ttyRecordBytes)
	key := []byte{'a'}
	for i := 0; i < ttyRecordBytes+1; i++ {
		r.Record(TTYIn, "keyboard", key)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Record(TTYIn, "keyboard", key)
	}
}
