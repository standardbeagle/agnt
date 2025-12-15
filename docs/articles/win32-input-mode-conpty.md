# Taming Windows Terminal's win32-input-mode in Go ConPTY Applications

When building [agnt](https://github.com/standardbeagle/agnt), a tool that gives AI coding agents browser superpowers, I hit a frustrating issue: my Ctrl+Y hotkey worked perfectly on macOS and Linux, but on Windows it produced garbage like `[89;21;25;1;8;1_` instead of toggling my overlay menu.

This article documents the debugging journey and the solution - a Go 1.23+ iterator-based parser that correctly handles Windows Terminal's win32-input-mode escape sequences.

## The Problem

I was wrapping AI coding tools (Claude, Gemini, etc.) in a pseudo-terminal to inject an overlay UI. The overlay listens for Ctrl+Y (byte `0x19`) to toggle a menu. Simple enough:

```go
if b == 0x19 { // Ctrl+Y
    overlay.Toggle()
}
```

On Unix, this worked immediately. On Windows with ConPTY, pressing Ctrl+Y produced:

```
[89;21;25;1;8;1_
```

What is this? It's a **win32-input-mode** escape sequence.

## What is win32-input-mode?

Windows Terminal introduced [win32-input-mode](https://github.com/microsoft/terminal/blob/main/doc/specs/%234999%20-%20Improved%20keyboard%20handling%20in%20Conpty.md) to provide richer keyboard input to ConPTY applications. Instead of sending raw bytes, it sends structured escape sequences:

```
ESC [ Vk ; Sc ; Uc ; Kd ; Cs ; Rc _
```

Where:
- **Vk** - Virtual key code (89 = 'Y')
- **Sc** - Scan code (21)
- **Uc** - Unicode character (25 = Ctrl+Y)
- **Kd** - Key down flag (1 = down, 0 = up)
- **Cs** - Control key state (8 = Ctrl held)
- **Rc** - Repeat count (1)

So `[89;21;25;1;8;1_` decodes to: "Y key pressed with Ctrl held, unicode value 25".

The unicode value `25` (0x19) is exactly what I needed - but it's buried in an escape sequence!

## The Debugging Journey

### Step 1: Isolate the Trigger

First, I needed to understand *when* Windows Terminal enables win32-input-mode. I created a diagnostic tool:

```go
// Direct stdin read - what bytes do we actually receive?
func main() {
    oldState, _ := term.MakeRaw(int(os.Stdin.Fd()))
    defer term.Restore(int(os.Stdin.Fd()), oldState)

    buf := make([]byte, 64)
    for {
        n, _ := os.Stdin.Read(buf)
        fmt.Printf("Read %d bytes: %q\n", n, buf[:n])
    }
}
```

**Finding 1:** Direct stdin reads showed raw bytes (0x19 for Ctrl+Y). Good.

**Finding 2:** When I created a ConPTY but didn't write its output anywhere, stdin still showed raw bytes.

**Finding 3:** When I wrote ConPTY output to stdout, suddenly stdin switched to win32-input-mode sequences!

The trigger: **writing PTY output to stdout causes Windows Terminal to enable win32-input-mode for the session.**

### Step 2: Attempt to Disable It

ConPTY supports a sequence to disable win32-input-mode:

```go
fmt.Fprint(os.Stdout, "\x1b[?9001l") // Disable win32-input-mode
```

This... didn't work reliably. Windows Terminal still sent the sequences in many scenarios.

### Step 3: Parse the Sequences

If we can't disable it, we parse it. First attempt:

```go
func parseWin32InputMode(data []byte) []byte {
    // Look for ESC [ ... _
    // Extract the Uc (unicode char) field
    // Return as raw bytes
}
```

This worked for single keypresses, but failed intermittently. The debug output revealed:

```
[win32] seq=89;21;25;1;8;1 -> byte 25 (0x19)  // First sequence parsed!
[win32] passthrough byte 27 (0x1b)            // Wait, why?
[win32] passthrough byte 91 (0x5b) '['        // This is another ESC[!
```

After parsing the first sequence correctly, subsequent sequences in the same buffer were passed through as raw bytes.

### Step 4: The Buffer Boundary Bug

The culprit: `os.Stdin.Read()` returns arbitrary chunks. An escape sequence can be split across reads:

```
Read 1: ...25;1;8;1_\x1b     <- ends with ESC
Read 2: [17;29;0;0;0;1_...   <- starts with [
```

When Read 1 ends with `\x1b`, my parser checked `data[i+1]` for `[`, but there was no next byte - it was in the next read! The ESC got passed through as a raw byte.

## The Solution: An Iterator with Remainder Handling

Go 1.23 introduced `iter.Seq`, which is perfect for this. The iterator:

1. Reads from stdin
2. Parses win32-input-mode sequences
3. Yields extracted bytes
4. Holds incomplete sequences for the next read

```go
// ScanWin32Input returns an iterator that reads from r and yields parsed bytes.
// Buffer boundaries are handled internally - incomplete sequences at the end
// of a read are held and combined with the next read.
func ScanWin32Input(r io.Reader) iter.Seq[byte] {
    return func(yield func(byte) bool) {
        var pending []byte
        buf := make([]byte, 256)

        for {
            n, err := r.Read(buf)
            if n > 0 {
                // Combine pending bytes with new data
                var data []byte
                if len(pending) > 0 {
                    data = make([]byte, len(pending)+n)
                    copy(data, pending)
                    copy(data[len(pending):], buf[:n])
                    pending = nil
                } else {
                    data = buf[:n]
                }

                // Parse and yield bytes
                parsed, remainder := parseWin32Sequences(data)
                pending = remainder

                for _, b := range parsed {
                    if !yield(b) {
                        return
                    }
                }
            }

            if err != nil {
                return
            }
        }
    }
}
```

The key is `parseWin32Sequences` returning a **remainder** - any incomplete sequence at the end of the buffer:

```go
func parseWin32Sequences(data []byte) (parsed []byte, remainder []byte) {
    var result []byte
    i := 0

    for i < len(data) {
        if data[i] == 0x1b {
            // ESC at end of buffer? Save as remainder
            if i+1 >= len(data) {
                return result, data[i:]
            }

            if data[i+1] == '[' {
                // Look for sequence terminator '_'
                end := findTerminator(data, i+2)

                if end > 0 {
                    // Complete sequence - parse it
                    b := extractUnicodeChar(data[i+2 : end])
                    if b > 0 {
                        result = append(result, b)
                    }
                    i = end + 1
                    continue
                }

                // No terminator found - might be incomplete
                if !hitInvalidChar {
                    return result, data[i:] // Save as remainder
                }
            }
        }

        // Regular byte - pass through
        result = append(result, data[i])
        i++
    }

    return result, nil
}
```

## Usage

With the iterator, consuming parsed input is clean:

```go
go func() {
    for b := range ScanWin32Input(os.Stdin) {
        inputCh <- b
    }
}()

// In main loop
for b := range inputCh {
    if b == 0x19 { // Ctrl+Y now works!
        overlay.Toggle()
    }
}
```

## Key Takeaways

1. **Windows Terminal enables win32-input-mode when you write PTY output to stdout.** There's no reliable way to disable it.

2. **Buffer boundaries will break naive parsers.** Always handle the case where a sequence is split across reads.

3. **Go 1.23+ iterators are perfect for streaming parsers.** The `iter.Seq` pattern encapsulates state cleanly.

4. **Build diagnostic tools.** The keylog utility that tested different scenarios (direct read, PTY only, PTY + output) was essential for isolating the trigger.

5. **Key-up events exist.** Win32-input-mode sends both key-down (Kd=1) and key-up (Kd=0) events. Only emit bytes for key-down.

## The Full Parser

The complete implementation handles:
- Buffer boundary splitting
- Key-down vs key-up filtering
- Focus in/out sequences (`ESC[I` and `ESC[O`)
- Invalid sequence recovery

You can find it in the [agnt repository](https://github.com/standardbeagle/agnt/blob/main/internal/overlay/input.go).

## Resources

- [Win32 Input Mode Spec](https://github.com/microsoft/terminal/blob/main/doc/specs/%234999%20-%20Improved%20keyboard%20handling%20in%20Conpty.md)
- [ConPTY Documentation](https://docs.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session)
- [agnt - Browser superpowers for AI coding agents](https://github.com/standardbeagle/agnt)
- [aymanbagabas/go-pty](https://github.com/aymanbagabas/go-pty) - Cross-platform PTY library for Go

---

*Building terminal applications that work across platforms? I'd love to hear about your experiences. Find me on [GitHub](https://github.com/standardbeagle).*

Tags: `#go` `#windows` `#terminal` `#conpty` `#tutorial`
