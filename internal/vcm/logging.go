package vcm

import (
	"fmt"
	"io"
	"sync"
)

// LogSemantic classifies the small, explicit tokens that logging may style.
// Hook payloads and operation details are deliberately never classified.
type LogSemantic uint8

const (
	LogContext LogSemantic = iota
	LogChanged
	LogWarning
	LogSuccess
	LogFailure
)

func (e *Engine) styleLog(semantic LogSemantic, text string) string {
	if e.Style == nil {
		return text
	}
	return e.Style(semantic, text)
}

func (e *Engine) logPrefix(operation, repository, path string) string {
	return e.styleLog(LogContext, fmt.Sprintf("[%s/%s @ ", operation, repository)) + path + e.styleLog(LogContext, "]")
}

func (e *Engine) logOperationOutcome(operation, repository, path, leading, outcome string, semantic LogSemantic, trailingFormat string, args ...any) {
	if e.Out == nil {
		return
	}
	fmt.Fprintf(e.Out, "%s %s%s%s\n", e.logPrefix(operation, repository, path), leading, e.styleLog(semantic, outcome), fmt.Sprintf(trailingFormat, args...))
}

func (e *Engine) logHook(repository, phase, id, path, status string, semantic LogSemantic, detail string) error {
	if e.Out == nil {
		return nil
	}
	prefix := e.logPrefix("hook/"+repository+"/"+phase, id, path)
	_, err := fmt.Fprintf(e.Out, "%s %s%s\n", prefix, e.styleLog(semantic, status), detail)
	return err
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
