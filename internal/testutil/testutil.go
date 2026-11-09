// Package testutil har hjälp för testerna: golden-filerna från TypeScript-versionen och sökvägar
// till testdata.
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

// Root är repots rot.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// Testdata är sökvägen till en fil under testdata/.
func Testdata(parts ...string) string {
	return filepath.Join(append([]string{Root(), "testdata"}, parts...)...)
}

// Golden läser en golden-fil (TypeScript-versionens svar) till v.
func Golden(t testing.TB, name string, v any) {
	t.Helper()
	ReadJSON(t, Testdata("golden", name), v)
}

// Local är testdata/local: sparade riktiga flöden och det som spelats in från dem. Katalogen
// versionshanteras inte (texterna tillhör andra), så testet hoppas över när den saknas.
func Local(t testing.TB) string {
	t.Helper()
	dir := Testdata("local")
	if _, err := os.Stat(filepath.Join(dir, "golden")); err != nil {
		t.Skip("testdata/local saknas: riktiga flöden testas bara lokalt (se AGENTS.md)")
	}
	return dir
}

// ReadJSON läser en JSON-fil till v.
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

// Stockholm sätter lokal tid till Europe/Stockholm under testet, som när golden-filerna spelades in.
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
