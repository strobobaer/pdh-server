package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"pdh"
)

// Server-Einstellungen → Cloudflare-Tunnel: Token und öffentlicher Hostname
// werden wie alle anderen Werte in der Datenbank gepflegt (Token verschlüsselt).
// Daraus erzeugt das PDH ein Installationsskript für Linux (systemd) bzw.
// Windows, das cloudflared installiert und als Dienst mit dem Token startet.
// Die Hostname-Zuordnung (Public Hostname → Service) liegt im Cloudflare-
// Dashboard; der Tunnel wird dort verwaltet („remotely managed“).

const (
	cfTokenKey  = "PDH_CLOUDFLARE_TUNNEL_TOKEN"
	cfHostKey   = "PDH_CLOUDFLARE_HOSTNAME"
	cfOriginKey = "PDH_CLOUDFLARE_ORIGIN"
	cfOriginDef = "http://localhost:8090"
)

var (
	cfTokenRe = regexp.MustCompile(`^[A-Za-z0-9+/=_.-]{40,}$`)
	cfHostRe  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// normalizeEnvValue: eingefügte Werte bereinigen, bevor sie geprüft werden –
// beim Token darf der ganze Befehl aus dem Dashboard eingefügt werden
// („cloudflared service install eyJ…“), beim Hostnamen eine Adresse.
func normalizeEnvValue(f envField, v string) string {
	switch f.Key {
	case cfTokenKey:
		if fields := strings.Fields(v); len(fields) > 0 {
			return fields[len(fields)-1]
		}
	case cfHostKey:
		v = strings.ToLower(strings.TrimSpace(v))
		if i := strings.Index(v, "://"); i >= 0 {
			v = v[i+3:]
		}
		if i := strings.IndexAny(v, "/:?#"); i >= 0 {
			v = v[:i]
		}
		return strings.TrimSuffix(v, ".")
	}
	return v
}

func validateCloudflare(f envField, v string) error {
	switch f.Key {
	case cfTokenKey:
		if !cfTokenRe.MatchString(v) {
			return fmt.Errorf("%s: das sieht nicht wie ein Tunnel-Token aus – im Cloudflare-Dashboard den langen Wert nach „--token“ bzw. „service install“ kopieren", f.Label)
		}
	case cfHostKey:
		if !cfHostRe.MatchString(v) {
			return fmt.Errorf("%s: bitte nur den Hostnamen eingeben, z. B. pdh.firma.de", f.Label)
		}
	}
	return nil
}

// cfSettings: Werte, mit denen das Skript erzeugt wird (gespeicherter Stand).
type cfSettings struct {
	Token, Host, Origin string
}

func (h *Handler) cloudflareSettings(ctx context.Context) cfSettings {
	tok, _ := h.nextStartValue(ctx, cfTokenKey)
	host, _ := h.nextStartValue(ctx, cfHostKey)
	origin, _ := h.nextStartValue(ctx, cfOriginKey)
	if origin == "" {
		origin = cfOriginDef
	}
	return cfSettings{Token: tok, Host: host, Origin: origin}
}

// CloudflareView: Angaben für Anleitung und Knöpfe im Reiter.
type CloudflareView struct {
	TokenSet           bool
	Host, Origin       string
	OriginScheme       string // HTTP/HTTPS – „Service Type“ im Dashboard
	OriginAddr         string // localhost:8090 – „URL“ im Dashboard
	PublicURLDifferent bool   // Öffentliche Adresse passt nicht zum Hostnamen
}

func (h *Handler) cloudflareView(ctx context.Context) *CloudflareView {
	s := h.cloudflareSettings(ctx)
	v := &CloudflareView{TokenSet: s.Token != "", Host: s.Host, Origin: s.Origin, OriginScheme: "HTTP", OriginAddr: "localhost:8090"}
	if u, err := url.Parse(s.Origin); err == nil && u.Host != "" {
		v.OriginScheme, v.OriginAddr = strings.ToUpper(u.Scheme), u.Host
	}
	if s.Host != "" {
		pub, _ := h.nextStartValue(ctx, "PDH_PUBLIC_URL")
		v.PublicURLDifferent = strings.TrimRight(pub, "/") != "https://"+s.Host
	}
	return v
}

// CloudflaredScriptWeb: GET /admin/server-config/cloudflared/script?os=linux|windows
// – Installationsskript mit eingesetztem Token zum Herunterladen.
func (h *Handler) CloudflaredScriptWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	s := h.cloudflareSettings(r.Context())
	if s.Token == "" {
		serverConfigRedirect(w, r, "cloudflare", "", errors.New("Zuerst den Tunnel-Token eintragen und speichern"))
		return
	}
	if !cfTokenRe.MatchString(s.Token) || (s.Host != "" && !cfHostRe.MatchString(s.Host)) {
		serverConfigRedirect(w, r, "cloudflare", "", errors.New("Tunnel-Token oder Hostname ungültig – bitte neu eintragen"))
		return
	}
	body, name := cloudflaredScript(r.URL.Query().Get("os"), s)
	who := ""
	if u := getUser(r); u != nil {
		who = u.Username
	}
	componentLog("server-config").Info().Str("benutzer", who).Str("datei", name).Msg("Cloudflare-Tunnel-Skript heruntergeladen")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = io.WriteString(w, body)
}

// cloudflaredScript: Skript und Dateiname für das Zielsystem.
func cloudflaredScript(osName string, s cfSettings) (string, string) {
	host := s.Host
	if host == "" {
		host = "(im Cloudflare-Dashboard festgelegt)"
	}
	rep := strings.NewReplacer("{{TOKEN}}", s.Token, "{{HOST}}", host, "{{ORIGIN}}", s.Origin,
		"{{VERSION}}", pdh.Version(), "{{DATE}}", time.Now().Format("02.01.2006 15:04"))
	if osName == "windows" {
		// PowerShell 5.1 liest UTF-8 nur mit BOM richtig; Windows-Zeilenenden
		return "\xef\xbb\xbf" + strings.ReplaceAll(rep.Replace(cfScriptWindows), "\n", "\r\n"), "pdh-cloudflared.ps1"
	}
	return rep.Replace(cfScriptLinux), "pdh-cloudflared.sh"
}

const cfScriptLinux = `#!/usr/bin/env bash
# PDH – Cloudflare-Tunnel einrichten (Linux mit systemd)
# Erzeugt von PDH {{VERSION}} am {{DATE}}.
# ACHTUNG: Diese Datei enthält den Tunnel-Token. Nach der Einrichtung löschen.
#
# Ausführen auf dem Server, auf dem das PDH läuft (auch bei Docker – auf dem Host):
#   sudo bash pdh-cloudflared.sh
set -Eeuo pipefail

TOKEN='{{TOKEN}}'
HOSTNAME_PUBLIC='{{HOST}}'
ORIGIN='{{ORIGIN}}'

ok()   { printf '\033[1;32m✔ %s\033[0m\n' "$*"; }
info() { printf '\033[1;34m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m! %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31m✘ %s\033[0m\n' "$*" >&2; exit 1; }

[[ ${EUID} -eq 0 ]] || fail "Bitte mit sudo ausführen: sudo bash $0"
command -v systemctl >/dev/null || fail "systemd fehlt – dieses Skript ist für Linux-Server mit systemd."
command -v curl >/dev/null || fail "curl fehlt – z. B. mit 'apt-get install -y curl' nachinstallieren."

# 1. cloudflared installieren (offizielle Pakete von GitHub)
if command -v cloudflared >/dev/null; then
  ok "cloudflared ist bereits installiert: $(cloudflared --version 2>&1 | head -n1)"
else
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    armv7l|armv6l|arm*) arch=arm ;;
    i386|i686) arch=386 ;;
    *) fail "Unbekannte Architektur $(uname -m)" ;;
  esac
  base="https://github.com/cloudflare/cloudflared/releases/latest/download"
  tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
  if command -v dpkg >/dev/null; then
    info "Installiere cloudflared (.deb, ${arch}) …"
    curl -fsSL -o "$tmp/cloudflared.deb" "$base/cloudflared-linux-${arch}.deb"
    dpkg -i "$tmp/cloudflared.deb"
  elif command -v rpm >/dev/null && [[ $arch != 386 ]]; then
    rarch=$arch; [[ $arch == amd64 ]] && rarch=x86_64; [[ $arch == arm64 ]] && rarch=aarch64
    info "Installiere cloudflared (.rpm, ${rarch}) …"
    curl -fsSL -o "$tmp/cloudflared.rpm" "$base/cloudflared-linux-${rarch}.rpm"
    rpm -Uvh "$tmp/cloudflared.rpm"
  else
    info "Installiere cloudflared nach /usr/local/bin …"
    curl -fsSL -o /usr/local/bin/cloudflared "$base/cloudflared-linux-${arch}"
    chmod 0755 /usr/local/bin/cloudflared
  fi
  ok "cloudflared installiert: $(cloudflared --version 2>&1 | head -n1)"
fi

# 2. Erreicht der Server das PDH?
if curl -fsS -m 5 -o /dev/null -k "${ORIGIN%/}/health"; then
  ok "PDH unter ${ORIGIN} erreichbar"
else
  warn "PDH unter ${ORIGIN} nicht erreichbar – läuft das PDH? Stimmt das Ziel im Cloudflare-Dashboard?"
fi

# 3. Dienst mit dem Token einrichten (ein vorhandener Tunnel-Dienst wird ersetzt)
if systemctl list-unit-files cloudflared.service >/dev/null 2>&1 && systemctl cat cloudflared.service >/dev/null 2>&1; then
  info "Vorhandenen cloudflared-Dienst ersetzen …"
  cloudflared service uninstall >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/cloudflared.service
  systemctl daemon-reload
fi
info "Richte den Tunnel-Dienst ein …"
cloudflared service install "$TOKEN"
systemctl enable --now cloudflared >/dev/null 2>&1 || true

# 4. Prüfen
for i in $(seq 1 15); do
  systemctl is-active --quiet cloudflared && break
  sleep 1
done
systemctl is-active --quiet cloudflared || { journalctl -u cloudflared -n 30 --no-pager; fail "Der Dienst cloudflared läuft nicht (Protokoll oben)."; }
ok "Tunnel-Dienst läuft (systemctl status cloudflared)"
sleep 5
if [[ $HOSTNAME_PUBLIC != \(* ]]; then
  if curl -fsS -m 15 -o /dev/null "https://${HOSTNAME_PUBLIC}/health"; then
    ok "PDH ist über https://${HOSTNAME_PUBLIC} erreichbar"
  else
    warn "https://${HOSTNAME_PUBLIC} antwortet noch nicht. Im Cloudflare-Dashboard den Public Hostname prüfen (Service: ${ORIGIN}); DNS kann ein paar Minuten brauchen."
  fi
fi
echo
ok "Fertig. Diese Datei enthält den Token – bitte löschen: rm -f $0"
`

const cfScriptWindows = `# PDH – Cloudflare-Tunnel einrichten (Windows)
# Erzeugt von PDH {{VERSION}} am {{DATE}}.
# ACHTUNG: Diese Datei enthält den Tunnel-Token. Nach der Einrichtung löschen.
#
# Ausführen in einer PowerShell „Als Administrator“ auf dem PDH-Server:
#   powershell -ExecutionPolicy Bypass -File .\pdh-cloudflared.ps1
#Requires -RunAsAdministrator
$ErrorActionPreference = 'Stop'
$Token    = '{{TOKEN}}'
$PublicHost = '{{HOST}}'
$Origin   = '{{ORIGIN}}'

function Ok($m)   { Write-Host "OK  $m" -ForegroundColor Green }
function Info($m) { Write-Host "==> $m" -ForegroundColor Cyan }
function Warn($m) { Write-Host "!   $m" -ForegroundColor Yellow }

[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

# 1. cloudflared installieren
function Find-Cloudflared {
  $c = Get-Command cloudflared -ErrorAction SilentlyContinue
  if ($c) { return $c.Source }
  foreach ($p in @("${env:ProgramFiles(x86)}\cloudflared\cloudflared.exe", "$env:ProgramFiles\cloudflared\cloudflared.exe")) {
    if (Test-Path $p) { return $p }
  }
  return $null
}
$exe = Find-Cloudflared
if ($exe) {
  Ok "cloudflared ist bereits installiert: $exe"
} else {
  $arch = if ([Environment]::Is64BitOperatingSystem) { 'amd64' } else { '386' }
  $msi = Join-Path $env:TEMP "cloudflared-windows-$arch.msi"
  Info "Lade cloudflared ($arch) …"
  Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-$arch.msi" -OutFile $msi
  Info "Installiere cloudflared …"
  & msiexec.exe /i $msi /qn /norestart | Out-Null  # Pipe wartet auf das Ende
  Remove-Item $msi -ErrorAction SilentlyContinue
  $exe = Find-Cloudflared
  if (-not $exe) { throw "cloudflared wurde nicht gefunden – Installation fehlgeschlagen." }
  Ok "cloudflared installiert: $exe"
}

# 2. Erreicht der Server das PDH?
try {
  Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 -Uri ($Origin.TrimEnd('/') + '/health') | Out-Null
  Ok "PDH unter $Origin erreichbar"
} catch { Warn "PDH unter $Origin nicht erreichbar – läuft das PDH? Stimmt das Ziel im Cloudflare-Dashboard?" }

# 3. Dienst mit dem Token einrichten (ein vorhandener Tunnel-Dienst wird ersetzt)
if (Get-Service -Name cloudflared -ErrorAction SilentlyContinue) {
  Info "Vorhandenen cloudflared-Dienst ersetzen …"
  Stop-Service cloudflared -ErrorAction SilentlyContinue
  & $exe service uninstall 2>$null | Out-Null
  Start-Sleep -Seconds 2
}
Info "Richte den Tunnel-Dienst ein …"
& $exe service install $Token
Start-Sleep -Seconds 3
Start-Service cloudflared -ErrorAction SilentlyContinue
Set-Service cloudflared -StartupType Automatic

# 4. Prüfen
$svc = Get-Service cloudflared -ErrorAction SilentlyContinue
if (-not $svc -or $svc.Status -ne 'Running') { throw "Der Dienst cloudflared läuft nicht – Ereignisanzeige (Anwendung) prüfen." }
Ok "Tunnel-Dienst läuft"
Start-Sleep -Seconds 5
if (-not $PublicHost.StartsWith('(')) {
  try {
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 15 -Uri "https://$PublicHost/health" | Out-Null
    Ok "PDH ist über https://$PublicHost erreichbar"
  } catch { Warn "https://$PublicHost antwortet noch nicht. Im Cloudflare-Dashboard den Public Hostname prüfen (Service: $Origin); DNS kann ein paar Minuten brauchen." }
}
Write-Host ""
Ok "Fertig. Diese Datei enthält den Token – bitte löschen: Remove-Item '$PSCommandPath'"
`

// cloudflaredDiagnose: läuft cloudflared auf diesem Server, und ist das PDH
// über den öffentlichen Hostnamen erreichbar?
func (h *Handler) cloudflaredDiagnose(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	line := func(icon, color, html string) {
		fmt.Fprintf(w, `<div style="color:%s"><i class="ti %s"></i> %s</div>`, color, icon, html)
	}
	s := h.cloudflareSettings(ctx)
	if s.Token == "" {
		line("ti-alert-circle", "var(--red)", "Kein Tunnel-Token gespeichert.")
	} else {
		line("ti-key", "var(--muted)", fmt.Sprintf("Tunnel-Token gespeichert (%d Zeichen).", len(s.Token)))
	}
	// cloudflared meldet seinen Zustand unter 127.0.0.1:20241–20245/ready
	client := &http.Client{Timeout: 800 * time.Millisecond}
	found := false
	for port := 20241; port <= 20245 && !found; port++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/ready", port), nil)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var st struct {
			ReadyConnections int `json:"readyConnections"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&st)
		resp.Body.Close()
		found = true
		if resp.StatusCode == http.StatusOK {
			line("ti-circle-check", "var(--green)", fmt.Sprintf("cloudflared läuft auf diesem Server – %d Verbindung(en) zu Cloudflare.", st.ReadyConnections))
		} else {
			line("ti-alert-triangle", "var(--amber)", "cloudflared läuft auf diesem Server, hat aber keine Verbindung zu Cloudflare (Token prüfen).")
		}
	}
	if !found {
		line("ti-info-circle", "var(--muted)", "Kein cloudflared auf diesem Rechner gefunden. Läuft das PDH in Docker, ist das normal – der Tunnel läuft dann auf dem Host.")
	}
	if s.Host == "" {
		line("ti-alert-circle", "var(--amber)", "Kein öffentlicher Hostname eingetragen – Erreichbarkeit nicht geprüft.")
		return
	}
	pub := "https://" + s.Host
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pub+"/health", nil)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		line("ti-alert-circle", "var(--red)", esc(pub)+" nicht erreichbar: "+esc(err.Error()))
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK && resp.Header.Get("Cf-Ray") != "":
		line("ti-circle-check", "var(--green)", "Das PDH ist über "+esc(pub)+" erreichbar (über Cloudflare).")
	case resp.StatusCode == http.StatusOK:
		line("ti-circle-check", "var(--green)", esc(pub)+" antwortet.")
	case resp.StatusCode == 530 || resp.StatusCode == 502 || resp.StatusCode == 1033:
		line("ti-alert-circle", "var(--red)", fmt.Sprintf("%s: Cloudflare meldet %d – der Tunnel ist nicht verbunden oder das Ziel (%s) im Dashboard stimmt nicht.", esc(pub), resp.StatusCode, esc(s.Origin)))
	default:
		line("ti-alert-triangle", "var(--amber)", fmt.Sprintf("%s antwortet mit %d: %s", esc(pub), resp.StatusCode, esc(strings.TrimSpace(string(body[:min(len(body), 160)])))))
	}
}
