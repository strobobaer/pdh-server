package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"pdh/pkg/config"
	"pdh/pkg/database"
)

// Verwaltung -> Server-Einstellungen: alle Werte, die frueher nur in der
// .env-Datei standen, in der Oberflaeche pflegen. Gespeichert wird in der
// Einstellungsdatei (config.EnvFilePath()); von aussen gesetzte Werte
// (Docker compose, systemd, Shell) haben Vorrang und sind hier nur lesbar.

type envField struct {
	Key, Label, Help, Type, Default string
	Options                         []string // fuer Type "select"
	Secret                          bool
	Restart                         bool // wirkt erst nach Neustart
}

type envGroup struct {
	Key, Label, Icon, Intro string
	Fields                  []envField
}

var envGroups = []envGroup{
	{"server", "Server", "ti-server", "Adresse und Betriebsart des PDH-Servers.", []envField{
		{Key: "PDH_PUBLIC_URL", Label: "Öffentliche Adresse", Help: "Wie Benutzer das PDH erreichen, z. B. https://pdh.firma.de – für Links in E-Mails und QR-Codes auf Etiketten.", Type: "url"},
		{Key: "PDH_SERVER_HOST", Label: "Lauscht auf", Help: "0.0.0.0 = alle Netzwerkkarten, 127.0.0.1 = nur lokal (hinter einem Reverse-Proxy).", Type: "text", Default: "0.0.0.0", Restart: true},
		{Key: "PDH_SERVER_PORT", Label: "Port", Type: "number", Default: "8090", Restart: true},
		{Key: "PDH_SERVER_ENV", Label: "Betriebsart", Help: "production = knappere Protokolle.", Type: "select", Options: []string{"production", "development"}, Default: "development", Restart: true},
	}},
	{"database", "Datenbank", "ti-database", "PostgreSQL-Verbindung. Änderungen werden vor dem Speichern getestet.", []envField{
		{Key: "PDH_DATABASE_HOST", Label: "Server", Type: "text", Default: "localhost", Restart: true},
		{Key: "PDH_DATABASE_PORT", Label: "Port", Type: "number", Default: "5432", Restart: true},
		{Key: "PDH_DATABASE_NAME", Label: "Datenbank", Type: "text", Restart: true},
		{Key: "PDH_DATABASE_USER", Label: "Benutzer", Type: "text", Restart: true},
		{Key: "PDH_DATABASE_PASSWORD", Label: "Passwort", Type: "text", Secret: true, Restart: true},
		{Key: "PDH_DATABASE_SSLMODE", Label: "Verschlüsselung (SSL)", Type: "select", Options: []string{"disable", "prefer", "require", "verify-ca", "verify-full"}, Default: "disable", Restart: true},
	}},
	{"security", "Sicherheit", "ti-shield-lock", "Schlüssel für Anmeldung und gespeicherte Passwörter.", []envField{
		{Key: "PDH_AUTH_JWTSECRET", Label: "Anmelde-Schlüssel (JWT)", Help: "Mindestens 32 Zeichen. Ein neuer Schlüssel meldet alle Benutzer ab.", Type: "text", Secret: true, Restart: true},
		{Key: "PDH_AUTH_TOKENDURATION", Label: "Anmeldung gültig (Stunden)", Type: "number", Default: "24", Restart: true},
		{Key: "PDH_CREDENTIALS_KEY", Label: "Schlüssel für Portal-Passwörter", Help: "Leer = aus dem Anmelde-Schlüssel abgeleitet. Nach dem ersten Speichern von Portal-Zugangsdaten NICHT mehr ändern – sonst sind sie unlesbar.", Type: "text", Secret: true},
	}},
	{"mail", "E-Mail", "ti-mail", "SMTP-Versand, z. B. für den täglichen Bestellvorschlag.", []envField{
		{Key: "PDH_SMTP_HOST", Label: "SMTP-Server", Type: "text"},
		{Key: "PDH_SMTP_PORT", Label: "Port", Type: "number", Default: "587"},
		{Key: "PDH_SMTP_TLS", Label: "Verschlüsselung", Help: "starttls = Port 587, tls = Port 465.", Type: "select", Options: []string{"starttls", "tls", "none"}, Default: "starttls"},
		{Key: "PDH_SMTP_USER", Label: "Benutzer", Type: "text"},
		{Key: "PDH_SMTP_PASSWORD", Label: "Passwort", Type: "text", Secret: true},
		{Key: "PDH_SMTP_FROM", Label: "Absender", Type: "email"},
	}},
	{"copilot", "Copilot (KI)", "ti-brain", "KI-Analyse von Störungen – lokal (Ollama) oder in der Cloud (Anthropic).", []envField{
		{Key: "PDH_COPILOT_BACKEND", Label: "Anbieter", Type: "select", Options: []string{"ollama", "anthropic"}, Default: "ollama", Restart: true},
		{Key: "PDH_COPILOT_OLLAMAURL", Label: "Ollama-Adresse", Type: "url", Default: "http://localhost:11434", Restart: true},
		{Key: "PDH_COPILOT_MODEL", Label: "Ollama-Modell", Type: "text", Default: "llama3.2", Restart: true},
		{Key: "PDH_COPILOT_ANTHROPICKEY", Label: "Anthropic-API-Schlüssel", Type: "text", Secret: true, Restart: true},
		{Key: "PDH_COPILOT_ANTHROPICMODEL", Label: "Anthropic-Modell", Type: "text", Default: "claude-sonnet-4-20250514", Restart: true},
	}},
	{"microsoft", "Microsoft 365", "ti-brand-windows", "Anmeldung mit Microsoft, Teams-Benachrichtigungen und Organisationsabgleich.", []envField{
		{Key: "PDH_MICROSOFT_TENANT_ID", Label: "Tenant-ID", Type: "text"},
		{Key: "PDH_MICROSOFT_CLIENT_ID", Label: "Client-ID", Type: "text"},
		{Key: "PDH_MICROSOFT_CLIENT_SECRET", Label: "Client-Secret", Type: "text", Secret: true},
		{Key: "PDH_MICROSOFT_REDIRECT_URL", Label: "Redirect-URL", Type: "url"},
		{Key: "PDH_MICROSOFT_TEAMS_SENDER_USER_ID", Label: "Teams: Absender-Benutzer-ID", Type: "text"},
		{Key: "PDH_MICROSOFT_TEAMS_ID", Label: "Teams: Team-ID", Type: "text"},
		{Key: "PDH_MICROSOFT_TEAMS_CHANNEL_ID", Label: "Teams: Kanal-ID", Type: "text"},
	}},
	{"nextcloud", "Nextcloud", "ti-cloud", "Dateiablage, Anmeldung und Deck-Karten in Nextcloud.", []envField{
		{Key: "PDH_NEXTCLOUD_ENABLED", Label: "Nextcloud-Ablage aktiv", Type: "bool", Restart: true},
		{Key: "PDH_NEXTCLOUD_BASEURL", Label: "Nextcloud-Adresse", Type: "url", Restart: true},
		{Key: "PDH_NEXTCLOUD_USERNAME", Label: "Benutzer", Type: "text", Restart: true},
		{Key: "PDH_NEXTCLOUD_PASSWORD", Label: "App-Passwort", Type: "text", Secret: true, Restart: true},
		{Key: "PDH_NEXTCLOUD_ROOTPATH", Label: "Stammordner", Type: "text", Restart: true},
		{Key: "PDH_NEXTCLOUD_SSO_SECRET", Label: "SSO-Schlüssel", Type: "text", Secret: true, Restart: true},
		{Key: "PDH_NEXTCLOUD_DECK_ENABLED", Label: "Deck-Abgleich aktiv", Type: "bool", Restart: true},
		{Key: "PDH_NEXTCLOUD_DECK_BOARD_ID", Label: "Deck: Board-ID", Type: "number", Restart: true},
		{Key: "PDH_NEXTCLOUD_DECK_STACK_TICKETS_ID", Label: "Deck: Stapel Tickets", Type: "number", Restart: true},
		{Key: "PDH_NEXTCLOUD_DECK_STACK_FAULTS_ID", Label: "Deck: Stapel Störungen", Type: "number", Restart: true},
		{Key: "PDH_NEXTCLOUD_DECK_STACK_MAINTENANCE_ID", Label: "Deck: Stapel Wartung", Type: "number", Restart: true},
	}},
	{"system", "Updates & Sicherung", "ti-refresh", "Update-Agent und Sicherungsordner.", []envField{
		{Key: "PDH_UPDATE_AGENT_URL", Label: "Update-Agent-Adresse", Type: "url"},
		{Key: "PDH_UPDATE_AGENT_TOKEN", Label: "Update-Agent-Token", Type: "text", Secret: true},
		{Key: "PDH_BACKUP_DIR", Label: "Sicherungsordner", Type: "text", Default: "backups"},
	}},
}

func envFieldByKey(key string) (envField, bool) {
	for _, g := range envGroups {
		for _, f := range g.Fields {
			if f.Key == key {
				return f, true
			}
		}
	}
	return envField{}, false
}

// SetRestartFunc: main.go stellt den Neustart bereit.
func (h *Handler) SetRestartFunc(fn func()) { h.restartFn = fn }

func (h *Handler) canServerConfig(r *http.Request) bool {
	return h.actorIsAdmin(r) && h.hasPerm(r, "system.server_config")
}

// ── Seite ────────────────────────────────────────────────────

type envFieldView struct {
	envField
	Value    string // nur fuer nicht geheime Felder
	IsSet    bool
	Source   string // env | file | default
	ReadOnly bool
}

type envGroupView struct {
	Key, Label, Icon, Intro string
	Fields                  []envFieldView
	External                int
}

type ServerConfigData struct {
	BaseData
	Tab            string
	Groups         []envGroupView
	EnvFile        string
	FileWritable   bool
	RestartPending []string
	CanRestart     bool
	Msg, Err       string
}

func (h *Handler) ServerConfigPage(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	path := config.EnvFilePath()
	file, _ := config.ReadEnvFile(path)
	d := ServerConfigData{
		BaseData: h.baseData(r, "server-config", "Server-Einstellungen", "Server"),
		Tab:      q.Get("tab"), EnvFile: path, RestartPending: h.restartPending, CanRestart: h.restartFn != nil,
		Msg: q.Get("msg"), Err: q.Get("err"),
	}
	if abs, err := absPath(path); err == nil {
		d.EnvFile = abs
	}
	d.FileWritable = fileWritable(path)
	for _, g := range envGroups {
		gv := envGroupView{Key: g.Key, Label: g.Label, Icon: g.Icon, Intro: g.Intro}
		for _, f := range g.Fields {
			v := envFieldView{envField: f}
			cur := os.Getenv(f.Key)
			_, inFile := file[f.Key]
			switch {
			case config.FromProcessEnv(f.Key):
				v.Source, v.ReadOnly = "env", true
				gv.External++
			case inFile:
				v.Source = "file"
			default:
				v.Source = "default"
			}
			v.IsSet = cur != ""
			if !f.Secret {
				v.Value = cur
				if v.Value == "" && v.Source == "default" {
					v.Value = f.Default
				}
			}
			gv.Fields = append(gv.Fields, v)
		}
		d.Groups = append(d.Groups, gv)
	}
	h.render(w, "server_config", d)
}

func serverConfigRedirect(w http.ResponseWriter, r *http.Request, tab, msg string, err error) {
	v := url.Values{"tab": {tab}}
	if err != nil {
		v.Set("err", err.Error())
	} else if msg != "" {
		v.Set("msg", msg)
	}
	http.Redirect(w, r, "/admin/server-config?"+v.Encode(), http.StatusSeeOther)
}

// collectEnvUpdates liest das Formular einer Gruppe und prueft die Werte.
func collectEnvUpdates(r *http.Request, g envGroup) (map[string]string, error) {
	updates := map[string]string{}
	for _, f := range g.Fields {
		if config.FromProcessEnv(f.Key) {
			continue // von aussen vorgegeben
		}
		raw, present := r.Form[f.Key]
		val := ""
		if present && len(raw) > 0 {
			val = strings.TrimSpace(raw[len(raw)-1])
		}
		if f.Type == "bool" {
			if r.FormValue(f.Key) == "true" {
				val = "true"
			} else {
				val = "false"
			}
		} else if f.Secret {
			if r.FormValue("clear_"+f.Key) == "1" {
				updates[f.Key] = ""
				continue
			}
			if val == "" {
				continue // leer = unveraendert
			}
		} else if !present {
			continue
		}
		if err := validateEnvValue(f, val); err != nil {
			return nil, err
		}
		if val != os.Getenv(f.Key) {
			updates[f.Key] = val
		}
	}
	return updates, nil
}

func validateEnvValue(f envField, v string) error {
	if strings.ContainsAny(v, "\n\r") {
		return fmt.Errorf("%s: keine Zeilenumbrüche erlaubt", f.Label)
	}
	if v == "" {
		return nil
	}
	switch f.Type {
	case "number":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("%s: bitte eine Zahl eingeben", f.Label)
		}
		if strings.HasSuffix(f.Key, "_PORT") && (n < 1 || n > 65535) {
			return fmt.Errorf("%s: Port zwischen 1 und 65535", f.Label)
		}
	case "url":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%s: bitte eine vollständige Adresse mit http:// oder https:// eingeben", f.Label)
		}
	case "email":
		if !strings.Contains(v, "@") {
			return fmt.Errorf("%s: bitte eine E-Mail-Adresse eingeben", f.Label)
		}
	case "select":
		for _, o := range f.Options {
			if o == v {
				return nil
			}
		}
		return fmt.Errorf("%s: ungültige Auswahl", f.Label)
	}
	if f.Key == "PDH_AUTH_JWTSECRET" && len(v) < 32 {
		return errors.New("Der Anmelde-Schlüssel muss mindestens 32 Zeichen lang sein")
	}
	return nil
}

// ServerConfigSaveWeb: POST /admin/server-config/{group}
func (h *Handler) ServerConfigSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	key := r.FormValue("group")
	var group *envGroup
	for i := range envGroups {
		if envGroups[i].Key == key {
			group = &envGroups[i]
		}
	}
	if group == nil {
		serverConfigRedirect(w, r, "server", "", errors.New("unbekannter Bereich"))
		return
	}
	updates, err := collectEnvUpdates(r, *group)
	if err == nil && group.Key == "security" && r.FormValue("new_jwt") == "1" && !config.FromProcessEnv("PDH_AUTH_JWTSECRET") {
		updates["PDH_AUTH_JWTSECRET"] = config.RandomSecret(32)
	}
	if err == nil && group.Key == "database" && len(updates) > 0 && r.FormValue("force") != "1" {
		err = testDBWith(r.Context(), updates)
	}
	if err != nil {
		serverConfigRedirect(w, r, group.Key, "", err)
		return
	}
	if len(updates) == 0 {
		serverConfigRedirect(w, r, group.Key, "Keine Änderungen.", nil)
		return
	}
	msg, err := h.applyEnvUpdates(r.Context(), updates, getUser(r).ID)
	serverConfigRedirect(w, r, group.Key, msg, err)
}

// applyEnvUpdates schreibt die Datei, uebernimmt die Werte sofort, soweit
// moeglich, und merkt sich, was erst nach einem Neustart wirkt.
func (h *Handler) applyEnvUpdates(ctx context.Context, updates map[string]string, userID string) (string, error) {
	path := config.EnvFilePath()
	if err := config.WriteEnvFile(path, updates); err != nil {
		return "", fmt.Errorf("Einstellungsdatei %s nicht beschreibbar: %w", path, err)
	}
	if err := config.ApplyEnvFile(path); err != nil {
		return "", err
	}
	var keys, restart []string
	for k := range updates {
		keys = append(keys, k)
		if f, ok := envFieldByKey(k); ok && f.Restart {
			restart = append(restart, f.Label)
		}
	}
	sort.Strings(keys)
	sort.Strings(restart)
	for _, l := range restart {
		if !containsStr(h.restartPending, l) {
			h.restartPending = append(h.restartPending, l)
		}
	}
	h.reloadLiveConfig(ctx)
	log.Info().Strs("keys", keys).Str("user", userID).Msg("server-einstellungen geaendert")
	if h.db != nil { // Sicherheits-Hinweis an alle Admins (ohne Werte)
		h.notifyAdmins(ctx, "⚙️ **Server-Einstellungen geändert** von "+h.chatUserName(ctx, userID)+": "+strings.Join(keys, ", "))
	}
	msg := fmt.Sprintf("Gespeichert (%d Wert(e)).", len(keys))
	if len(restart) > 0 {
		msg += " Wirksam nach einem Neustart: " + strings.Join(restart, ", ") + "."
	} else {
		msg += " Sofort wirksam."
	}
	return msg, nil
}

// reloadLiveConfig uebernimmt Werte, die ohne Neustart wirken.
func (h *Handler) reloadLiveConfig(ctx context.Context) {
	cfg, err := config.Load()
	if err != nil {
		log.Warn().Err(err).Msg("server-einstellungen: neu laden")
		return
	}
	h.ConfigureMail(cfg.Mail)
	h.ConfigureUpdates(cfg.Update.AgentURL, cfg.Update.AgentToken, h.buildCommit)
	h.ConfigureMicrosoft(MicrosoftOAuthConfig{
		ClientID: cfg.Microsoft.ClientID, ClientSecret: cfg.Microsoft.ClientSecret, RedirectURL: cfg.Microsoft.RedirectURL,
		TenantID: cfg.Microsoft.TenantID, TeamsSenderUserID: cfg.Microsoft.TeamsSenderUserID,
		TeamsID: cfg.Microsoft.TeamsID, TeamsChannelID: cfg.Microsoft.TeamsChannelID,
	})
	if h.db != nil {
		h.StartPurchaseReportSchedule(ctx)
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// dbConfigWith: aktuelle Datenbank-Einstellungen mit geaenderten Werten.
func dbConfigWith(updates map[string]string) *config.DatabaseConfig {
	get := func(k, def string) string {
		if v, ok := updates[k]; ok {
			return v
		}
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	port, _ := strconv.Atoi(get("PDH_DATABASE_PORT", "5432"))
	return &config.DatabaseConfig{
		Host: get("PDH_DATABASE_HOST", "localhost"), Port: port, User: get("PDH_DATABASE_USER", ""),
		Password: get("PDH_DATABASE_PASSWORD", ""), Name: get("PDH_DATABASE_NAME", ""), SSLMode: get("PDH_DATABASE_SSLMODE", "disable"),
	}
}

func testDBWith(ctx context.Context, updates map[string]string) error {
	if err := database.TestConnection(ctx, dbConfigWith(updates)); err != nil {
		return fmt.Errorf("Verbindungstest fehlgeschlagen – nicht gespeichert (%v). Mit „Trotzdem speichern“ lässt sich das übergehen", err)
	}
	return nil
}

// ServerConfigTestWeb: POST /admin/server-config/test/{what} (database|mail) - htmx
func (h *Handler) ServerConfigTestWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	ok := func(msg string) {
		fmt.Fprintf(w, `<span style="color:var(--green)"><i class="ti ti-circle-check"></i> %s</span>`, esc(msg))
	}
	fail := func(err error) {
		fmt.Fprintf(w, `<span style="color:var(--red)"><i class="ti ti-alert-circle"></i> %s</span>`, esc(err.Error()))
	}
	switch r.URL.Query().Get("what") {
	case "database":
		g := envGroups[1]
		updates, err := collectEnvUpdates(r, g)
		if err != nil {
			fail(err)
			return
		}
		cfg := dbConfigWith(updates)
		if err := database.TestConnection(r.Context(), cfg); err != nil {
			fail(fmt.Errorf("keine Verbindung zu %s:%d/%s: %v", cfg.Host, cfg.Port, cfg.Name, err))
			return
		}
		ok(fmt.Sprintf("Verbindung zu %s:%d/%s als %s erfolgreich.", cfg.Host, cfg.Port, cfg.Name, cfg.User))
	case "mail":
		if !h.mailConfigured() {
			fail(errors.New("E-Mail ist noch nicht vollständig eingerichtet – zuerst speichern"))
			return
		}
		u := getUser(r)
		var to string
		_ = h.db.QueryRow(r.Context(), `SELECT email FROM users WHERE id = $1::uuid`, u.ID).Scan(&to)
		if to == "" {
			fail(errors.New("dein Benutzerkonto hat keine E-Mail-Adresse"))
			return
		}
		if err := h.sendMail([]string{to}, "PDH – Testnachricht", "<p>Diese Testnachricht bestätigt, dass der E-Mail-Versand des PDH funktioniert.</p><p>Gesendet "+time.Now().Format("02.01.2006 15:04")+"</p>"); err != nil {
			fail(err)
			return
		}
		ok("Testnachricht an " + to + " gesendet.")
	default:
		fail(errors.New("unbekannter Test"))
	}
}

// ServerRestartWeb: POST /admin/server-config/restart
func (h *Handler) ServerRestartWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) || h.restartFn == nil {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	u := getUser(r)
	log.Warn().Str("user", u.ID).Msg("neustart ueber server-einstellungen angefordert")
	if h.db != nil {
		h.notifyAdmins(r.Context(), "🔄 **Server-Neustart** angefordert von "+strings.TrimSpace(u.FirstName+" "+u.LastName)+" – das PDH ist gleich wieder erreichbar.")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Neustart</title>
<body style="font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#0f172a;color:#e2e8f0">
<div style="text-align:center"><h2>PDH startet neu …</h2><p id="s">Bitte einen Moment Geduld.</p></div>
<script>
let n=0;function p(){fetch('/health',{cache:'no-store'}).then(r=>{if(r.ok&&n>2){location.href='/admin/server-config?msg='+encodeURIComponent('Neustart abgeschlossen.')}else{n++;setTimeout(p,1500)}}).catch(()=>{n++;setTimeout(p,1500)})}
setTimeout(p,2500);
</script>`)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(700 * time.Millisecond)
		h.restartFn()
	}()
}

func absPath(p string) (string, error) {
	if strings.HasPrefix(p, "/") {
		return p, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return p, err
	}
	return wd + string(os.PathSeparator) + p, nil
}

// fileWritable prueft, ob die Einstellungsdatei (bzw. ihr Ordner) beschreibbar ist.
func fileWritable(path string) bool {
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		f.Close()
		return true
	}
	if _, err := os.Stat(path); err == nil {
		return false
	}
	dir := "."
	if i := strings.LastIndexAny(path, `/\`); i > 0 {
		dir = path[:i]
	}
	tmp, err := os.CreateTemp(dir, ".pdh-write-test-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	return true
}
