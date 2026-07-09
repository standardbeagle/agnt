package daemon

import (
	"sync"
	"testing"
	"time"
)

// handleDeliveryFailure pushes DeliverAt into the future on every failed attempt,
// while SESSION TASKS (ToJSON, via ListTasks) and the persister (MarshalJSON)
// read it from other goroutines. time.Time is multi-word, so this is a data race
// even when the values happen to look sane.
//
// Run under -race: without the fix this reports a write/read race on DeliverAt.
func TestScheduledTask_DeliverAtIsRaceFree(t *testing.T) {
	task := NewScheduledTask("t1", "sess", "msg", "/proj",
		time.Now().Add(time.Hour), time.Now(), TaskStatusPending)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: the retry path, rescheduling the task after each failed attempt.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			task.SetDeliverAt(time.Now().Add(time.Duration(i) * time.Millisecond))
		}
		close(stop)
	}()

	// Reader: SESSION TASKS serving the agent.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = task.ToJSON()
			}
		}
	}()

	// Reader: the persister marshalling the same pointer it was handed.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := task.MarshalJSON(); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()

	// Reader: checkDueTasks deciding whether the task is due.
	wg.Add(1)
	go func() {
		defer wg.Done()
		now := time.Now()
		for {
			select {
			case <-stop:
				return
			default:
				_ = task.GetDeliverAt().Before(now)
			}
		}
	}()

	wg.Wait()
}

// A round trip through the accessors must preserve the instant.
func TestScheduledTask_DeliverAtRoundTrip(t *testing.T) {
	want := time.Now().Add(90 * time.Second).Truncate(time.Second)
	task := NewScheduledTask("t2", "sess", "msg", "/proj", want, time.Now(), TaskStatusPending)

	if got := task.GetDeliverAt(); !got.Equal(want) {
		t.Fatalf("GetDeliverAt() = %v, want %v", got, want)
	}

	next := want.Add(time.Minute)
	task.SetDeliverAt(next)
	if got := task.GetDeliverAt(); !got.Equal(next) {
		t.Fatalf("after SetDeliverAt, GetDeliverAt() = %v, want %v", got, next)
	}

	// The wire form must still carry it.
	if got := task.ToJSON()["deliver_at"]; got != next.Format(time.RFC3339) {
		t.Errorf("ToJSON deliver_at = %v, want %v", got, next.Format(time.RFC3339))
	}
}
