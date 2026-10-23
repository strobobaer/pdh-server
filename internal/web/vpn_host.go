package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pdh"
)

// WireGuard auf dem Host (systemd-Dienst pdh-wireguard): Server-Einstellungen →
// VPN → Installationsskript. Das Skript installiert wireguard-tools, legt den
// Dienst (scripts/pdh-wireguard-daemon.sh, eingebettet als pdh.WireGuardDaemon) und den
// Austauschordner an, öffnet den UDP-Port in ufw/firewalld und startet den Dienst.
// Bei Docker ist der Ordner in die App eingebunden (compose.yaml:
// PDH_WIREGUARD_HOST_DIR → /app/wireguard), sonst ist es config/wireguard des PDH.

const vpnDefHostDir = "/srv/pdh-wireguard"

// vpnHostView: was das Installationsskript braucht.
type vpnHostView struct {
	Dir      string // Austauschordner auf dem Host
	UID, GID int    // Benutzer der PDH-App (muss schreiben können)
	Docker   bool
}

func vpnHostInfo() vpnHostView {
	v := vpnHostView{UID: os.Getuid(), GID: os.Getgid()}
	if d := strings.TrimSpace(os.Getenv("PDH_WIREGUARD_HOST_DIR")); d != "" {
		v.Dir, v.Docker = d, true
	} else if abs, err := filepath.Abs(vpnDir()); err == nil && strings.HasPrefix(abs, "/") {
		v.Dir = abs
	} else {
		v.Dir = vpnDefHostDir // z. B. Entwicklung unter Windows
	}
	if v.UID < 0 { // Windows
		v.UID, v.GID = 10001, 10001
	}
	return v
}

// vpnInstallScript: Installationsskript für den Host (Linux mit systemd).
func vpnInstallScript(host vpnHostView, port int) string {
	return strings.NewReplacer(
		"{{DIR}}", host.Dir, "{{UID}}", strconv.Itoa(host.UID), "{{GID}}", strconv.Itoa(host.GID), "{{PORT}}", strconv.Itoa(port),
		"{{VERSION}}", pdh.Version(), "{{DATE}}", time.Now().Format("02.01.2006 15:04"), "{{DAEMON}}", strings.TrimRight(pdh.WireGuardDaemon, "\n"),
	).Replace(vpnScriptLinux)
}

// VPNScriptWeb: GET /admin/server-config/vpn/script – pdh-wireguard.sh
func (h *Handler) VPNScriptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.vpnAdminGuard(w, r) {
		return
	}
	s, _ := h.vpnLoadSettings(r.Context())
	if s.Port == 0 {
		s.Port = vpnDefPort
	}
	h.vpnLog(r.Context(), getUser(r).ID, "", "", "script", "Installationsskript heruntergeladen")
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="pdh-wireguard.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(vpnInstallScript(vpnHostInfo(), s.Port)))
}

const vpnScriptLinux = `#!/usr/bin/env bash
# PDH – WireGuard-VPN ins Maschinennetz auf dem Host einrichten (Linux mit systemd)
# Erzeugt von PDH {{VERSION}} am {{DATE}}. Enthält keine Schlüssel.
#
# Ausführen auf dem Server, auf dem das PDH läuft (auch bei Docker – auf dem Host):
#   sudo bash pdh-wireguard.sh
# Nach einem PDH-Update oder einer Änderung des Ports erneut ausführen.

# mit „sh“ aufgerufen: mit bash neu starten (dash kennt Arrays und (( )) nicht)
if [ -z "${BASH_VERSION:-}" ]; then exec bash "$0" "$@"; fi
set -Eeuo pipefail

DIR='{{DIR}}'
PDH_UID='{{UID}}'
PDH_GID='{{GID}}'
PORT='{{PORT}}'

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }

[[ ${EUID} -eq 0 ]] || fail "Bitte mit sudo ausführen: sudo bash $0"
command -v systemctl >/dev/null || fail "systemd fehlt – dieses Skript ist für Linux-Server mit systemd."

# 1. Werkzeuge installieren
need=()
command -v wg >/dev/null && command -v wg-quick >/dev/null || need+=(wireguard-tools)
command -v iptables >/dev/null || need+=(iptables)
command -v ip >/dev/null || need+=(iproute2)
if ((${#need[@]})); then
  info "Installiere ${need[*]} …"
  if command -v apt-get >/dev/null; then
    apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "${need[@]}"
  elif command -v dnf >/dev/null; then
    dnf install -y "${need[@]/iproute2/iproute}"
  elif command -v zypper >/dev/null; then
    zypper --non-interactive install "${need[@]}"
  else
    fail "Paketverwaltung unbekannt – bitte ${need[*]} von Hand installieren."
  fi
fi
ok "WireGuard-Werkzeuge: $(wg --version 2>&1 | head -n1)"

# 2. WireGuard im Kernel?
modprobe wireguard 2>/dev/null || true
if ip link add pdhtest type wireguard 2>/dev/null; then
  ip link del pdhtest
  ok "WireGuard im Kernel verfügbar"
else
  fail "WireGuard fehlt im Kernel (Linux ab 5.6 nötig, z. B. Ubuntu 22.04/24.04, Debian 12)."
fi

# 3. Austauschordner mit dem PDH
mkdir -p "$DIR"
chown "$PDH_UID:$PDH_GID" "$DIR"
chmod 700 "$DIR"
ok "Ordner $DIR (gehört dem PDH-Benutzer $PDH_UID)"

# 4. Dienst pdh-wireguard
info "Richte den Dienst pdh-wireguard ein …"
cat >/usr/local/bin/pdh-wireguard <<'PDH_WG_DAEMON'
{{DAEMON}}
PDH_WG_DAEMON
chmod 0755 /usr/local/bin/pdh-wireguard
cat >/etc/systemd/system/pdh-wireguard.service <<PDH_WG_UNIT
[Unit]
Description=PDH WireGuard (VPN ins Maschinennetz)
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
Environment=PDH_WIREGUARD_DIR=$DIR
Environment=PDH_UID=$PDH_UID
Environment=PDH_GID=$PDH_GID
ExecStart=/usr/local/bin/pdh-wireguard
Restart=always
RestartSec=5
TimeoutStopSec=20

[Install]
WantedBy=multi-user.target
PDH_WG_UNIT
systemctl daemon-reload
systemctl enable pdh-wireguard >/dev/null 2>&1
systemctl restart pdh-wireguard

# 5. UDP-Port in der Firewall des Servers öffnen
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active'; then
  ufw allow "$PORT/udp" comment 'PDH WireGuard' >/dev/null && ok "ufw: UDP-Port $PORT geöffnet"
elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
  firewall-cmd --permanent --add-port="$PORT/udp" >/dev/null && firewall-cmd --reload >/dev/null && ok "firewalld: UDP-Port $PORT geöffnet"
fi

# 6. Prüfen
for i in $(seq 1 15); do
  if systemctl is-active --quiet pdh-wireguard && [[ -f $DIR/heartbeat ]] && (( $(date +%s) - $(cat "$DIR/heartbeat") < 30 )); then
    break
  fi
  sleep 1
done
systemctl is-active --quiet pdh-wireguard || { journalctl -u pdh-wireguard -n 30 --no-pager; fail "Der Dienst pdh-wireguard läuft nicht (Protokoll oben)."; }
ok "Dienst läuft (systemctl status pdh-wireguard, Protokoll: journalctl -u pdh-wireguard)"
[[ -f $DIR/error ]] && warn "Meldung des Dienstes: $(cat "$DIR/error")"
echo
ok "Fertig. Jetzt im Internet-Router den UDP-Port $PORT auf diesen Server weiterleiten und in PDH unter Server-Einstellungen → VPN einschalten."
`
