package main

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
)

func TestJoinAttachWorkers_EOFOrDetachJoinsFrameAndResize(t *testing.T) {
	for _, name := range []string{"eof", "detach"} {
		t.Run(name, func(t *testing.T) {
			frame := make(chan error, 1)
			input := make(chan error, 1)
			input <- io.EOF
			var cancelOnce sync.Once
			canceled, stopped := 0, 0
			err := joinAttachWorkers(func() {
				cancelOnce.Do(func() { canceled++; frame <- context.Canceled })
			}, func() error { t.Fatal("input interrupt called after input completed"); return nil }, func() { stopped++ }, frame, input)
			if err != nil || canceled != 1 || stopped != 1 {
				t.Fatalf("err=%v cancel=%d stop=%d", err, canceled, stopped)
			}
		})
	}
}

func TestJoinAttachWorkers_BlockedInputInterruptedAndJoinedOnFrameError(t *testing.T) {
	want := errors.New("frame failed")
	frame := make(chan error, 1)
	input := make(chan error, 1)
	frame <- want
	joinedInput := false
	err := joinAttachWorkers(func() {}, func() error {
		joinedInput = true
		input <- errors.New("operation aborted")
		return nil
	}, func() {}, frame, input)
	if !errors.Is(err, want) || !joinedInput {
		t.Fatalf("err=%v joinedInput=%v", err, joinedInput)
	}
}

func TestJoinAttachWorkers_FrameErrorWinsAfterLocalEOF(t *testing.T) {
	want := errors.New("server rejected attach")
	frame := make(chan error, 1)
	input := make(chan error, 1)
	input <- io.EOF
	err := joinAttachWorkers(func() { frame <- want }, func() error { return nil }, func() {}, frame, input)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want frame error %v", err, want)
	}
}

func TestJoinAttachWorkers_FrameErrorMasksInterruptFailure(t *testing.T) {
	frameErr := errors.New("frame failed")
	interruptErr := errors.New("CancelIoEx failed")
	frame := make(chan error, 1)
	input := make(chan error, 1)
	frame <- frameErr
	err := joinAttachWorkers(func() {}, func() error {
		input <- context.Canceled
		return interruptErr
	}, func() {}, frame, input)
	if !errors.Is(err, frameErr) {
		t.Fatalf("error = %v, want frame error %v", err, frameErr)
	}
}

func TestAttachCleanup_AllWorkersJoinBeforeRestoreAndCleanupIsIdempotent(t *testing.T) {
	var events []string
	frame := make(chan error, 1)
	input := make(chan error, 1)
	frame <- nil
	err := runPreparedConsole(func() bool { return true }, func() (func(), error) {
		var once sync.Once
		return func() { once.Do(func() { events = append(events, "restore") }) }, nil
	}, func(restore func()) error {
		err := joinAttachWorkers(func() { events = append(events, "cancel") }, func() error {
			events = append(events, "interrupt")
			input <- context.Canceled
			return nil
		}, func() { events = append(events, "resize-joined") }, frame, input)
		restore() // panic-safe relay cleanup may restore before outer defer
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cancel", "interrupt", "resize-joined", "restore"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}
