package pdh

import _ "embed"

// ESPHomeVersion ist die ESPHome-Version, mit der ESPPDH die Firmware baut
// (scripts/esphome.version, eine Zeile). scripts/install-esphome.sh installiert
// genau diese Version; internal/modules/esppdh vergleicht damit die installierte.
//
//go:embed scripts/esphome.version
var ESPHomeVersion string
