// Package mastorss håller versionen, som läses ur filen VERSION när programmet byggs.
package mastorss

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var version string

// Version är versionen, till exempel "1.1.0".
func Version() string { return strings.TrimSpace(version) }

// UserAgent skickas med varje anrop.
func UserAgent() string { return "masto-rss/" + Version() }
