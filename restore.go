package pdh

import _ "embed"

// RestoreScript ist das Wiederaufbau-Skript des Ausfallschutzes
// (scripts/pdh-restore.sh). Der Update-Agent legt es nach jeder Grundsicherung
// in den Sicherungsordner; das PDH bietet es unter Server-Einstellungen →
// Ausfallschutz zum Herunterladen an.
//
//go:embed scripts/pdh-restore.sh
var RestoreScript string

// VMPrepareScript bereitet eine frische Ubuntu-VM vor – für eine neue
// Installation (--neu) oder den Wiederaufbau aus dem Ausfallschutz
// (--wiederaufbau, scripts/pdh-vm-prepare.sh).
//
//go:embed scripts/pdh-vm-prepare.sh
var VMPrepareScript string

// HAJoinScript fügt einen weiteren Server oder den Zeugen dem Ausfallschutz
// (pdh-ha) hinzu (scripts/pdh-ha-join.sh). Das PDH bietet es zusammen mit dem
// Beitrittscode unter Server-Einstellungen → Ausfallschutz an.
//
//go:embed scripts/pdh-ha-join.sh
var HAJoinScript string
