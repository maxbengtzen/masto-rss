// Package mastorss holds the version, which is read from the file VERSION when the program is built.
package mastorss

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var version string

// Version returns the version, for example "1.1.0".
func Version() string { return strings.TrimSpace(version) }

// UserAgent is sent with every request.
func UserAgent() string { return "masto-rss/" + Version() }
