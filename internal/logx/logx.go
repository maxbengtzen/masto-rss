// Package logx writes log lines with the time in the container's time zone (TZ).
package logx

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Logger writes log lines. The zero value writes to stdout.
type Logger struct {
	mu    sync.Mutex
	Out   io.Writer
	Debug bool
}

// Printf writes a line with a timestamp, like "2026-10-26 13:59:00 text".
func (l *Logger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.Out
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// Debugf only writes with LOG_LEVEL=debug.
func (l *Logger) Debugf(format string, args ...any) {
	if l.Debug {
		l.Printf(format, args...)
	}
}
