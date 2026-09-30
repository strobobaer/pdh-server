// Package pdh stellt die Programmversion bereit (Datei VERSION im
// Wurzelverzeichnis, Aenderungen in CHANGELOG.md). Beide werden in das
// Programm eingebettet - VERSION ist die einzige Stelle, an der die
// Versionsnummer gepflegt wird.
package pdh

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

//go:embed CHANGELOG.md
var Changelog string

// Version liefert die Versionsnummer, z. B. "0.15.0".
func Version() string { return strings.TrimSpace(versionFile) }
