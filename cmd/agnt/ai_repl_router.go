package main

import (
	"bytes"
	"io"
)

// REPLAdapter bridges the overlay InputRouter to the LineEditor.
// It implements PtyReadWriter so InputRouter can pass non-hotkey bytes
// to the line editor instead of a child PTY.
type REPLAdapter struct {
	editor *LineEditor
}

// NewREPLAdapter creates a new REPL adapter wrapping the given line editor.
func NewREPLAdapter(editor *LineEditor) *REPLAdapter {
	return &REPLAdapter{editor: editor}
}

// Write receives raw bytes from InputRouter when the overlay is inactive.
// Each byte is fed to the LineEditor for processing.
func (r *REPLAdapter) Write(p []byte) (n int, err error) {
	for _, b := range p {
		r.editor.Feed(b)
	}
	return len(p), nil
}

// Read always returns EOF. The REPL adapter doesn't produce output
// that InputRouter would read — output flows through the overlay writer chain.
func (r *REPLAdapter) Read(p []byte) (n int, err error) {
	return 0, io.EOF
}

// rawModeWriter translates bare \n to \r\n for raw mode terminals.
// In raw mode, \n only moves down without returning to column 1.
// This wrapper lets existing code using fmt.Fprintln/\n work correctly.
type rawModeWriter struct {
	out io.Writer
}

func newRawModeWriter(out io.Writer) *rawModeWriter {
	return &rawModeWriter{out: out}
}

func (w *rawModeWriter) Write(p []byte) (n int, err error) {
	// Fast path: no newlines at all
	if !bytes.ContainsRune(p, '\n') {
		return w.out.Write(p)
	}

	// Replace bare \n (not preceded by \r) with \r\n
	var buf bytes.Buffer
	buf.Grow(len(p) + 16)
	for i := 0; i < len(p); i++ {
		if p[i] == '\n' && (i == 0 || p[i-1] != '\r') {
			buf.WriteByte('\r')
		}
		buf.WriteByte(p[i])
	}
	_, err = w.out.Write(buf.Bytes())
	return len(p), err
}
