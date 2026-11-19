package pdh

import _ "embed"

// ESPHomeVersion ist die Mindestversion von ESPHome für ESPPDH
// (scripts/esphome.version, eine Zeile): die Grundumgebung, die mit dem PDH kommt.
// scripts/install-esphome.sh installiert genau diese Version; internal/modules/esppdh
// vergleicht damit die installierte – nur eine ältere gilt als „veraltet“. Eine neuere,
// selbst geholte Version (täglicher Lauf in internal/web/esppdh_update.go) bleibt.
//
//go:embed scripts/esphome.version
var ESPHomeVersion string
