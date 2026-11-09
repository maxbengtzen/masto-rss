// Package logx skriver loggrader med tid i containerns tidszon (TZ).
package logx

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Logger skriver loggrader. Nollvärdet skriver till stdout.
type Logger struct {
	mu    sync.Mutex
	Out   io.Writer
	Debug bool
}

// Printf skriver en rad med tidsstämpel, som "2026-10-26 13:59:00 text".
func (l *Logger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.Out
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// Debugf skrivs bara med LOG_LEVEL=debug.
func (l *Logger) Debugf(format string, args ...any) {
	if l.Debug {
		l.Printf(format, args...)
	}
}
