// Command masto-rss läser RSS-, Atom-, RDF- och JSON-flöden och postar nya poster till Mastodon.
// `masto-rss healthcheck` är Dockers hälsokontroll: den lyckas om huvudloopen skrivit sin
// hälsofil de senaste tre minuterna.
package main

import (
	"fmt"
	"os"
	"time"
	_ "time/tzdata"

	"github.com/maxbengtzen/masto-rss/internal/app"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(app.HeartbeatFile, time.Now()))
	}
	os.Exit(app.Main(os.Args[1:], os.Environ(), os.Stdout, os.Stderr, app.Options{}))
}

// healthcheck gör samma sak som `find /tmp/masto-rss.alive -mmin -3` gjorde: filen ska finnas och
// vara yngre än tre minuter, räknat i hela sekunder.
func healthcheck(path string, now time.Time) int {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if age := now.Unix() - info.ModTime().Unix(); age >= 180 {
		fmt.Fprintf(os.Stderr, "%s skrevs för %d s sedan; huvudloopen står still\n", path, age)
		return 1
	}
	return 0
}
