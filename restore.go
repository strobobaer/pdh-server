package pdh

import _ "embed"

// RestoreScript ist das Wiederaufbau-Skript des Ausfallschutzes
// (scripts/pdh-restore.sh). Der Update-Agent legt es nach jeder Grundsicherung
// in den Sicherungsordner; das PDH bietet es unter Server-Einstellungen →
// Ausfallschutz zum Herunterladen an.
//
//go:embed scripts/pdh-restore.sh
var RestoreScript string
