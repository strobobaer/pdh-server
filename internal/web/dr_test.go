package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdh"
	"pdh/internal/core/users"
)

func TestDRTarget(t *testing.T) {
	if got := drTarget("/mnt/pdh/nas", "PDH-Srv_01"); got != "/mnt/pdh/nas/PDH-Ausfallschutz/pdh-srv-01" {
		t.Errorf("drTarget = %s", got)
	}
	if got := drTarget("/mnt/pdh/nas", ""); got != "/mnt/pdh/nas/PDH-Ausfallschutz/server" {
		t.Errorf("leerer Hostname: %s", got)
	}
}

func TestDBIsLocal(t *testing.T) {
	for _, h := range []string{"", "localhost", "127.0.0.1", "::1", "/var/run/postgresql"} {
		if !dbIsLocal(h) {
			t.Errorf("%q gilt nicht als lokal", h)
		}
	}
	for _, h := range []string{"db", "10.0.0.5", "pg.firma.de"} {
		if dbIsLocal(h) {
			t.Errorf("%q gilt als lokal", h)
		}
	}
}

func TestDRBuildConfig(t *testing.T) {
	f := url.Values{"enabled": {"1"}, "base_hour": {"3"}, "keep_days": {"30"}, "file_sync_minutes": {"15"}, "wal_max_minutes": {"2"}}
	c, err := drBuildConfig(f, "/mnt/pdh/nas", "srv1", "/var/www/nextcloud", true)
	if err != nil {
		t.Fatal(err)
	}
	want := drConfig{Enabled: true, Target: "/mnt/pdh/nas/PDH-Ausfallschutz/srv1", BaseHour: 3, KeepDays: 30,
		FileSyncMinutes: 15, WALMaxMinutes: 2, NextcloudDir: "/var/www/nextcloud"}
	if c != want {
		t.Fatalf("%+v", c)
	}
	if _, err := drBuildConfig(f, "/mnt/pdh/nas", "srv1", "", false); err == nil || !strings.Contains(err.Error(), "Datenbank auf diesem Server") {
		t.Fatalf("entfernte Datenbank: %v", err)
	}
	if _, err := drBuildConfig(f, "", "srv1", "", true); err == nil {
		t.Fatal("Einschalten ohne Laufwerk angenommen")
	}
	off := url.Values{}
	if c, err := drBuildConfig(off, "", "srv1", "", false); err != nil || c.Enabled || c.KeepDays != 14 || c.WALMaxMinutes != 5 {
		t.Fatalf("ausgeschaltet mit Standardwerten: %+v %v", c, err)
	}
}

func TestDRSendConfig(t *testing.T) {
	var got drConfig
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/dr/config" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "falsch", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer agent.Close()
	h := &Handler{updateAgentURL: agent.URL, updateAgentToken: "tok"}
	c := drConfig{Enabled: true, Target: "/mnt/pdh/nas/PDH-Ausfallschutz/srv1", BaseHour: 2, KeepDays: 14, FileSyncMinutes: 60, WALMaxMinutes: 5}
	if err := h.drSendConfig(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatalf("Agent erhielt %+v", got)
	}
}

func TestDRSettingsForbidden(t *testing.T) {
	h := &Handler{}
	for _, fn := range []http.HandlerFunc{h.DRSettingsWeb, h.DRRunWeb, h.DRVMWeb, h.DRRestoreScriptWeb} {
		req := httptest.NewRequest(http.MethodPost, "/admin/server-config/dr/settings", strings.NewReader("enabled=1"))
		req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "u1", Role: users.Role("user")}))
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("ohne Recht: Status %d", rec.Code)
		}
	}
}

func TestDRAlerts(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	fresh := drStatus{Config: drConfig{Enabled: true, FileSyncMinutes: 60}, LastBase: now.Add(-time.Hour), LastFileSync: now.Add(-10 * time.Minute)}
	if a := drAlerts(fresh, now.AddDate(0, 0, -1), 7, now); len(a) != 0 {
		t.Fatalf("frisch: %v", a)
	}
	off := fresh
	off.Config.Enabled, off.Error = false, "x"
	if a := drAlerts(off, time.Time{}, 7, now); len(a) != 0 {
		t.Fatalf("ausgeschaltet: %v", a)
	}
	cases := map[string]func(*drStatus) (time.Time, string){
		"fehler":   func(s *drStatus) (time.Time, string) { s.Error = "Netzlaufwerk nicht erreichbar"; return time.Time{}, "Fehler: Netzlaufwerk nicht erreichbar" },
		"spool":    func(s *drStatus) (time.Time, string) { s.SpoolBytes, s.SpoolFiles = 3<<30, 190; return time.Time{}, "WAL-Dateien stauen sich (190 Dateien)" },
		"spoolalt": func(s *drStatus) (time.Time, string) { s.SpoolFiles, s.SpoolOldest = 2, now.Add(-61*time.Minute); return time.Time{}, "WAL-Dateien stauen sich (2 Dateien)" },
		"base":     func(s *drStatus) (time.Time, string) { s.LastBase = now.Add(-27 * time.Hour); return time.Time{}, "Letzte Grundsicherung ist älter als 26 Stunden" },
		"files":    func(s *drStatus) (time.Time, string) { s.LastFileSync = now.Add(-181 * time.Minute); return time.Time{}, "Dateiabgleich hängt seit" },
		"vm":       func(s *drStatus) (time.Time, string) { return now.AddDate(0, 0, -8), "VM-Sicherung ist 8 Tage alt" },
	}
	for name, mod := range cases {
		st := fresh
		vm, want := mod(&st)
		a := drAlerts(st, vm, 7, now)
		if len(a) != 1 || !strings.Contains(a[0], want) {
			t.Errorf("%s: %v", name, a)
		}
	}
}

func TestDRPaneRenders(t *testing.T) {
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	v := drView{DBLocal: true, DriveKey: "nas", Drives: []networkDrive{{Key: "nas", Name: "NAS Sicherung", UseBackup: true}},
		Status: drStatus{OK: true, Supported: true, Archiving: true, Config: drConfig{Enabled: true, Target: "/mnt/pdh/nas/PDH-Ausfallschutz/srv1",
			BaseHour: 2, KeepDays: 14, FileSyncMinutes: 60, WALMaxMinutes: 5}, Sources: []string{"uploads", "nextcloud-data", "n8n-vol-n8n_data"},
			Missing: []string{"n8n nicht gefunden"}, Bases: []drBase{{Name: "20261024T020000Z", Bytes: 1 << 20}}},
		Alerts: []string{"Letzte Grundsicherung ist älter als 26 Stunden"}, VMMaxDays: 7}
	var b strings.Builder
	if err := bindLang(c, "de").ExecuteTemplate(&b, "dr-admin", v); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`action="/admin/server-config/dr/settings"`, `action="/admin/server-config/dr/run"`, `value="base"`, `value="files"`,
		`action="/admin/server-config/dr/vm"`, `href="/admin/server-config/dr/restore-script"`, "NAS Sicherung", "20261024T020000Z",
		"nextcloud-data", "n8n nicht gefunden", "Letzte Grundsicherung ist älter als 26 Stunden", "Zugangsdaten", "sudo bash pdh-restore.sh"} {
		if !strings.Contains(out, want) {
			t.Errorf("Reiter enthält %q nicht", want)
		}
	}
	b.Reset()
	_ = bindLang(c, "de").ExecuteTemplate(&b, "dr-admin", drView{AgentErr: "Ausfallschutz-Sicherung nur bei systemd-Installationen (Docker ist das Testsystem)"})
	if !strings.Contains(b.String(), "nur bei systemd-Installationen") || strings.Contains(b.String(), `/dr/run"`) {
		t.Error("Docker-Hinweis fehlt bzw. Knöpfe trotzdem da")
	}
}

func TestDRFetchStatusTimeout(t *testing.T) {
	block := make(chan struct{})
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer agent.Close()
	defer close(block)
	old := drAgentTimeout
	drAgentTimeout = 50 * time.Millisecond
	defer func() { drAgentTimeout = old }()
	h := &Handler{updateAgentURL: agent.URL, updateAgentToken: "tok"}
	start := time.Now()
	if _, err := h.drFetchStatus(context.Background()); err == nil {
		t.Fatal("hängender Agent ohne Fehler")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Zeitbegrenzung greift nicht")
	}
}

// Review #8: nie erfolgreiche Grundsicherung bzw. Abgleich seit dem Einschalten melden.
func TestDRAlertsNeverSucceeded(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	st := drStatus{Config: drConfig{Enabled: true, FileSyncMinutes: 60}, EnabledSince: now.Add(-27 * time.Hour)}
	got := strings.Join(drAlerts(st, time.Time{}, 7, now), " | ")
	if !strings.Contains(got, "Noch keine Grundsicherung") || !strings.Contains(got, "Noch kein Dateiabgleich") {
		t.Fatalf("Alarme: %s", got)
	}
	st.EnabledSince = now.Add(-10 * time.Minute)
	if a := drAlerts(st, time.Time{}, 7, now); len(a) != 0 {
		t.Fatalf("frisch eingeschaltet: %v", a)
	}
}

// Review #2: PDH sagt eingeschaltet, Agent nicht (z. B. state.json verloren) → Alarm.
func TestDRAgentMismatch(t *testing.T) {
	if a := drAgentMismatch(true, drStatus{}); len(a) != 1 || !strings.Contains(a[0], "Update-Agent") {
		t.Fatalf("%v", a)
	}
	if a := drAgentMismatch(true, drStatus{Config: drConfig{Enabled: true}}); len(a) != 0 {
		t.Fatalf("%v", a)
	}
	if a := drAgentMismatch(false, drStatus{}); len(a) != 0 {
		t.Fatalf("%v", a)
	}
}

func TestVMPrepareScript(t *testing.T) {
	s := pdh.VMPrepareScript
	if bash, err := exec.LookPath("bash"); err == nil {
		f := filepath.Join(t.TempDir(), "vm.sh")
		_ = os.WriteFile(f, []byte(s), 0o644)
		if out, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("Syntaxfehler %v %s", err, out)
		}
	}
	for _, want := range []string{"--neu", "--wiederaufbau", "systemd-detect-virt", "qemu-guest-agent", "open-vm-tools",
		"Europe/Berlin", "credentials=/root/.pdh-share-cred", "/etc/fstab", "mfsymlinks", "PDH-Ausfallschutz",
		"pdh-restore.sh", "CREATE ROLE", "install-update-agent.sh", "setup-code.txt", "ufw allow 8090/tcp"} {
		if !strings.Contains(s, want) {
			t.Errorf("pdh-vm-prepare.sh ohne %q", want)
		}
	}
	if strings.Contains(s, ",password=") || strings.Contains(s, "-o password") {
		t.Error("Freigabe-Passwort gehört nicht in Mount-Optionen/fstab")
	}
}

func TestDRVMScriptDownload(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/admin/server-config/dr/vm-script", nil)
	req = req.WithContext(context.WithValue(req.Context(), "user", &users.User{ID: "u1", Role: users.Role("user")}))
	rec := httptest.NewRecorder()
	h.DRVMScriptWeb(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ohne Recht: %d", rec.Code)
	}
	tmpl := loadTestTemplates(t)
	c, _ := tmpl.Clone()
	var b strings.Builder
	_ = bindLang(c, "de").ExecuteTemplate(&b, "dr-admin", drView{DBLocal: true, Status: drStatus{OK: true, Supported: true}})
	if !strings.Contains(b.String(), `href="/admin/server-config/dr/vm-script"`) {
		t.Error("Reiter ohne Download des VM-Skripts")
	}
}

// Verbund: Nach einer Übernahme ist die Sicherung im PDH (gespiegelte
// Datenbank) an, im Agenten des neuen aktiven Servers aber nie eingeschaltet
// worden – drHeal schickt die gespeicherten Werte an den eigenen Agenten.
func TestDRHealFromDatabase(t *testing.T) {
	t.Setenv("PDH_DATABASE_HOST", "localhost")
	var got drConfig
	calls := 0
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer agent.Close()
	h := &Handler{updateAgentURL: agent.URL, updateAgentToken: "tok"}
	form := drFormKeep(url.Values{"base_hour": {"3"}, "keep_days": {"7"}, "file_sync_minutes": {"30"}, "wal_max_minutes": {"5"}, "drive": {"x"}}).Encode()
	if strings.Contains(form, "drive") {
		t.Fatalf("Formularwerte: %s", form)
	}
	healed, err := h.drHeal(context.Background(), drStatus{}, form, "/mnt/pdh/nas", "/var/www/nextcloud")
	if err != nil || !healed || calls != 1 {
		t.Fatalf("geheilt=%v err=%v Aufrufe=%d", healed, err, calls)
	}
	if !got.Enabled || got.BaseHour != 3 || got.KeepDays != 7 || got.FileSyncMinutes != 30 || !strings.HasPrefix(got.Target, "/mnt/pdh/nas/") {
		t.Fatalf("Agent erhielt %+v", got)
	}
	// schon eingeschaltet: nichts senden
	if healed, err := h.drHeal(context.Background(), drStatus{Config: drConfig{Enabled: true}}, form, "/mnt/pdh/nas", ""); healed || err != nil || calls != 1 {
		t.Fatalf("erneut gesendet: %v %v %d", healed, err, calls)
	}
	// Laufwerk hier unbekannt: Fehler statt halber Konfiguration
	if _, err := h.drHeal(context.Background(), drStatus{}, form, "", ""); err == nil || calls != 1 {
		t.Fatalf("ohne Laufwerk: %v", err)
	}
}
