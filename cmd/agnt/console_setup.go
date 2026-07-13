package main

import (
	"fmt"
	"sync"
)

type consoleModeCalls struct {
	get func(uintptr) (uint32, error)
	set func(uintptr, uint32) error
}

// setupWindowsConsoleModes applies the same mode transition as x/term's
// Windows MakeRaw, but transactionally so every syscall is injectable and a
// partially applied failure restores the exact captured modes.
func setupWindowsConsoleModes(stdin, stdout uintptr, calls consoleModeCalls) (func(), error) {
	inputMode, err := calls.get(stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin console mode: %w", err)
	}
	outputMode, outputErr := calls.get(stdout)
	rawInput := inputMode &^ uint32(0x0001|0x0002|0x0004) // processed, line, echo
	rawInput |= 0x0200                                    // ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := calls.set(stdin, rawInput); err != nil {
		_ = calls.set(stdin, inputMode)
		return nil, fmt.Errorf("enable virtual terminal input: %w", err)
	}
	if outputErr == nil {
		if err := calls.set(stdout, outputMode|0x0004); err != nil { // ENABLE_VIRTUAL_TERMINAL_PROCESSING
			_ = calls.set(stdout, outputMode)
			_ = calls.set(stdin, inputMode)
			return nil, fmt.Errorf("enable virtual terminal output: %w", err)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = calls.set(stdin, inputMode)
			if outputErr == nil {
				_ = calls.set(stdout, outputMode)
			}
		})
	}, nil
}

func runPreparedConsole(isConsole func() bool, prepare func() (func(), error), run func(func()) error) error {
	if !isConsole() {
		return fmt.Errorf("agnt attach: stdin is not a Windows console; redirected input is unsupported")
	}
	restore, err := prepare()
	if err != nil {
		return err
	}
	defer restore()
	return run(restore)
}
