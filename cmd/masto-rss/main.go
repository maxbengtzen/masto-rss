// Command masto-rss reads RSS, Atom, RDF and JSON feeds and posts new items to Mastodon.
// `masto-rss healthcheck` is Docker's health check: it succeeds if the main loop has written its
// health file within the last three minutes.
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

// healthcheck does the same as `find /tmp/masto-rss.alive -mmin -3` did: the file must exist and
// be younger than three minutes, counted in whole seconds.
func healthcheck(path string, now time.Time) int {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if age := now.Unix() - info.ModTime().Unix(); age >= 180 {
		fmt.Fprintf(os.Stderr, "%s was written %d s ago; the main loop is stuck\n", path, age)
		return 1
	}
	return 0
}
