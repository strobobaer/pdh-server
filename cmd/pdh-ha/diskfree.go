package main

import (
	"strconv"
)

// freeSpace misst den freien Platz (in Tests ersetzbar).
var freeSpace = freeSpaceOS

// pdhInstallDir: Ersatz, wenn der Ordner der Datenbank unbekannt ist.
const pdhInstallDir = "/opt/pdh-server"

// diskHBLocked: Herzschlag-Feld disk – "free:<Bytes>" auf dem Dateisystem
// der PostgreSQL-Daten (sonst /opt/pdh-server), "unknown" wenn nicht messbar.
func (d *daemon) diskHBLocked() string {
	paths := []string{}
	if pa := d.pgActorLocked(); pa != nil && pa.pg != nil && pa.pg.c.DataDir != "" {
		paths = append(paths, pa.pg.c.DataDir)
	}
	paths = append(paths, pdhInstallDir)
	for _, p := range paths {
		if n, ok := freeSpace(p); ok {
			return "free:" + strconv.FormatInt(n, 10)
		}
	}
	return "unknown"
}
