package vcm

import (
	"fmt"
	"io"
	"sync"
)

func (e *Engine) logOperation(operation, repository, path, format string, args ...any) {
	if e.Out == nil {
		return
	}
	fmt.Fprintf(e.Out, "[%s/%s @ %s] %s\n", operation, repository, path, fmt.Sprintf(format, args...))
}

func (e *Engine) logHook(repository, phase, id, path, message string) {
	if e.Out == nil {
		return
	}
	fmt.Fprintf(e.Out, "[hook/%s/%s/%s @ %s] %s\n", repository, phase, id, path, message)
}

func abbreviateRevision(revision string) string {
	const length = 12
	if len(revision) <= length {
		return revision
	}
	return revision[:length]
}

// prefixedLineWriter combines hook stdout and stderr into one synchronized
// stream and adds context once per logical line.
type prefixedLineWriter struct {
	mu      sync.Mutex
	out     io.Writer
	prefix  string
	pending []byte
	err     error
}

func newPrefixedLineWriter(out io.Writer, prefix string) *prefixedLineWriter {
	if out == nil {
		out = io.Discard
	}
	return &prefixedLineWriter{out: out, prefix: prefix}
}

func (w *prefixedLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	w.pending = append(w.pending, p...)
	for {
		newline := -1
		for i, b := range w.pending {
			if b == '\n' {
				newline = i
				break
			}
		}
		if newline < 0 {
			break
		}
		line := append([]byte(w.prefix), w.pending[:newline+1]...)
		if _, w.err = w.out.Write(line); w.err != nil {
			return 0, w.err
		}
		w.pending = w.pending[newline+1:]
	}
	return len(p), nil
}

func (w *prefixedLineWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	if len(w.pending) == 0 {
		return nil
	}
	line := make([]byte, 0, len(w.prefix)+len(w.pending)+1)
	line = append(line, w.prefix...)
	line = append(line, w.pending...)
	line = append(line, '\n')
	_, w.err = w.out.Write(line)
	w.pending = nil
	return w.err
}
