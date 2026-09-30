package pdh

import "embed"

// Static enthaelt die App-Symbole aus web/static (Favicon, Apple-Touch-Icon,
// Web-App-Manifest). Erzeugt mit: go run ./scripts/favicon
//
//go:embed web/static
var Static embed.FS
