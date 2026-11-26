// Package testutil has helpers for the tests: the golden files from the TypeScript version and
// paths to test data.
package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	_ "time/tzdata"
)

// Root is the repository root.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// Testdata is the path to a file under testdata/.
func Testdata(parts ...string) string {
	return filepath.Join(append([]string{Root(), "testdata"}, parts...)...)
}

// Golden reads a golden file (the TypeScript version's output) into v.
func Golden(t testing.TB, name string, v any) {
	t.Helper()
	ReadJSON(t, Testdata("golden", name), v)
}

// Local is testdata/local: saved real feeds and what was recorded from them. The directory isn't
// under version control (the texts belong to others), so the test is skipped when it's missing.
func Local(t testing.TB) string {
	t.Helper()
	dir := Testdata("local")
	if _, err := os.Stat(filepath.Join(dir, "golden")); err != nil {
		t.Skip("testdata/local is missing: real feeds are only tested locally (see AGENTS.md)")
	}
	return dir
}

// ReadJSON reads a JSON file into v.
func ReadJSON(t testing.TB, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// Stockholm sets local time to Europe/Stockholm for the test, as when the golden files were recorded.
func Stockholm(t testing.TB) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	return loc
}
