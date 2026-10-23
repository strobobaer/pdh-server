package pdh

import _ "embed"

// WireGuardDaemon ist der systemd-Dienst pdh-wireguard für den Host
// (scripts/pdh-wireguard.sh). Das Installationsskript aus Server-Einstellungen
// → VPN legt ihn nach /usr/local/bin/pdh-wireguard (internal/web/vpn_host.go).
//
//go:embed scripts/pdh-wireguard.sh
var WireGuardDaemon string
