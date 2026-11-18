//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	u := systemdUnit("/usr/local/bin/pdh-data-client")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/pdh-data-client run",
		"Restart=always",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"RestartPreventExitStatus=3",
		"StartLimitBurst=",
		"ReadWritePaths=/var/lib/pdh-data-client /etc/pdh-data-client",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("Dienstdatei enthält %q nicht:\n%s", want, u)
		}
	}
}
