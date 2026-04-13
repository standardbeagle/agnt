package process

// lineWriter wraps a RingBuffer and calls a callback for each complete line.
// Incomplete lines are buffered until a newline arrives or flush is called.
type lineWriter struct {
	buf       *RingBuffer
	callback  OutputCallback
	processID string
	partial   []byte
}

// newLineWriter creates a lineWriter that writes to buf and delivers lines to cb.
func newLineWriter(buf *RingBuffer, processID string, cb OutputCallback) *lineWriter {
	return &lineWriter{
		buf:       buf,
		callback:  cb,
		processID: processID,
	}
}

// Write implements io.Writer. Data is written to the underlying RingBuffer
// and split into lines for the callback.
func (lw *lineWriter) Write(p []byte) (int, error) {
	n, err := lw.buf.Write(p)

	start := 0
	for i, b := range p {
		if b == '\n' {
			line := string(lw.partial) + string(p[start:i])
			lw.partial = lw.partial[:0]
			lw.callback(lw.processID, line)
			start = i + 1
		}
	}
	if start < len(p) {
		lw.partial = append(lw.partial, p[start:]...)
	}

	return n, err
}

// flush delivers any remaining partial line to the callback.
func (lw *lineWriter) flush() {
	if len(lw.partial) > 0 {
		lw.callback(lw.processID, string(lw.partial))
		lw.partial = lw.partial[:0]
	}
}
