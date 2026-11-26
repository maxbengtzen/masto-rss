package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The health check only succeeds if the file exists and is younger than three minutes, like
// find -mmin -3.
func TestHealthcheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alive")
	now := time.Now()
	if healthcheck(path, now) != 1 {
		t.Error("a missing file must fail")
	}
	os.WriteFile(path, nil, 0o644)
	for age, want := range map[time.Duration]int{0: 0, 179 * time.Second: 0, 180 * time.Second: 1, 10 * time.Minute: 1} {
		mtime := now.Add(-age)
		os.Chtimes(path, mtime, mtime)
		if got := healthcheck(path, now); got != want {
			t.Errorf("%v old: %d, want %d", age, got, want)
		}
	}
}
