package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"pdh"
	"pdh/pkg/appsettings"
)

const updateRepository = "strobobaer/pdh-server"

// updateBranch ist der Update-Zweig (PDH_UPDATE_BRANCH, Standard main) – derselbe,
// den install_update.sh holt. Ungültige Namen fallen auf main zurück.
func updateBranch() string {
	b := strings.TrimSpace(os.Getenv("PDH_UPDATE_BRANCH"))
	if b == "" || strings.HasPrefix(b, "-") || strings.Contains(b, "..") || !updateBranchRe.MatchString(b) {
		return "main"
	}
	return b
}

var updateBranchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

type CoreSettingsPageData struct {
	CompletionDepartments string      // Abschluss-Assistent: Abteilungen fuer "wer war dabei"
	DepartmentOptions     []string    // vorhandene Abteilungen (Vorschlaege)
	CanCode               bool        // Link „Code-Editor“ (/code/, codeserver_proxy.go) – nur Admins
	CanPKI                bool        // Link „Zertifikate (Nextcloud)“ (pki_web.go)
	AdminTools            []adminTool // n8n, pgAdmin unter /n8n/, /pgadmin/ (admin_tools_proxy.go)
	InfraRespDefault      bool        // Verantwortliche der Anlage in neue Vorgänge übernehmen (migrations/129)
	BaseData
	AutoCheckEnabled    bool
	CheckInterval       int
	CurrentVersion      string
	LatestVersion       string
	NewReleases         []ChangelogRelease
	CurrentCommit       string
	LatestCommit        string
	ComparisonURL       string
	ComparisonStatus    string
	ComparedCommitCount int
	ComparisonCommits   []UpdateCommitView
	LastChecked         string
	LastCheckError      string
	UpdateAvailable     bool
	UpdateCheckStale    bool // Vergleich gehört zu einem anderen Stand oder Update-Zweig (A1)
	AgentConfigured     bool
	AgentReachable      bool
	AgentStatus         string
	UpdateCluster       bool     // Verbund: „Update im Verbund installieren“ (maint_window.go)
	UpdatePassive       bool     // dieser Server ist nicht der aktive – das Update steuert der aktive
	UpdateKindText      string   // Update-Art aus dem Wartungsfenster
	UpdateAsk           string   // Rückfrage „Nicht alles grün …“ (pdh-ha meldet 409)
	UpdateAskHints      []string // Hinweise dazu
	UpdateAskToken      string   // Einmal-Token für „Trotzdem aktualisieren“
	AgentOutput         string
	NC                  *ncUpdateView // Kachel „Nextcloud-Updates“ (nextcloud_updates.go)
	Notice              string

	DefaultDueDaysFault       int
	DefaultDueDaysTicket      int
	DefaultDueDaysTask        int
	DefaultDueDaysMaintenance int

	EasyMode              bool         // QR-Meldung ohne Anmeldung (easy_mode.go)
	Defaults              DefaultsView // Standards für alle (user_defaults.go)
	IdleTimeoutMinutes    int          // Auto-Logout (früher unter Rollen & Rechte)
	OverrideTimeout       int
	BoardPickTerminalOnly bool             // Leitstand: Personenauswahl nur am Terminal (global_dashboard_actor.go)
	Push                  pushSettingsView // Push-Benachrichtigungen (push.go)
}

type UpdateCommitView struct {
	SHA     string
	URL     string
	Message string
	Date    string
}

type githubComparisonResponse struct {
	Status   string                `json:"status"`
	AheadBy  int                   `json:"ahead_by"`
	BehindBy int                   `json:"behind_by"`
	Commits  []githubCommitSummary `json:"commits"`
}

type githubCommitSummary struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

type updateAgentStatus struct {
	Running    bool      `json:"running"`
	Success    bool      `json:"success"`
	Output     string    `json:"output"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

func (h *Handler) ConfigureUpdates(agentURL, token, buildCommit string) {
	h.updateAgentURL = strings.TrimRight(strings.TrimSpace(agentURL), "/")
	h.updateAgentToken = strings.TrimSpace(token)
	h.buildCommit = strings.TrimSpace(buildCommit)
}

func (h *Handler) StartUpdateChecker(ctx context.Context) {
	go func() {
		if !h.maintSkip("update-pruefung") {
			h.checkUpdateIfDue(ctx)
		}
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if h.maintSkip("update-pruefung") {
					continue
				}
				h.checkUpdateIfDue(ctx)
			}
		}
	}()
}

func (h *Handler) CoreSettingsPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	enabled := h.getUpdateSetting(ctx, "update_auto_check_enabled", "true") == "true"
	interval, _ := strconv.Atoi(h.getUpdateSetting(ctx, "update_check_interval_hours", "24"))
	if interval < 1 || interval > 168 {
		interval = 24
	}
	data := CoreSettingsPageData{
		BaseData:         h.baseData(r, "core-settings", "Core-Einstellungen", "Systemverwaltung"),
		AutoCheckEnabled: enabled,
		CheckInterval:    interval,
		CurrentVersion:   pdh.Version(),
		CurrentCommit:    shortCommit(h.buildCommit),
		LastCheckError:   h.getUpdateSetting(ctx, "update_last_check_error", ""),
		AgentConfigured:  h.updateAgentURL != "" && h.updateAgentToken != "",
		Notice:           r.URL.Query().Get("notice"),

		DefaultDueDaysFault:       appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysFault, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysTicket:      appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTicket, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysTask:        appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysTask, appsettings.DefaultDueDaysFallback),
		DefaultDueDaysMaintenance: appsettings.GetInt(ctx, h.db, appsettings.KeyDefaultDueDaysMaintenance, appsettings.DefaultDueDaysFallback),
		EasyMode:                  h.easyModeEnabled(ctx),
		Defaults:                  h.defaultsView(r),
		IdleTimeoutMinutes:        h.rbac.IdleTimeoutMinutes(),
		OverrideTimeout:           h.rbac.OverrideTimeoutMinutes(),
		Push:                      h.pushSettingsView(ctx),
		BoardPickTerminalOnly:     h.boardPickTerminalOnly(ctx),
	}
	data.CompletionDepartments = strings.Join(h.completionDepartments(ctx), ", ")
	if rows, err := h.db.Query(ctx, `SELECT DISTINCT TRIM(department) FROM users WHERE active AND TRIM(COALESCE(department, '')) <> '' ORDER BY 1`); err == nil {
		for rows.Next() {
			var d string
			if rows.Scan(&d) == nil {
				data.DepartmentOptions = append(data.DepartmentOptions, d)
			}
		}
		rows.Close()
	}
	chk := h.loadUpdateCheck(ctx)
	data.applyUpdateCheck(chk, h.buildCommit, updateBranch())
	if !data.UpdateCheckStale {
		_ = json.Unmarshal([]byte(h.getUpdateSetting(ctx, "update_comparison_commits", "[]")), &data.ComparisonCommits)
		data.LatestVersion, data.NewReleases = h.availableReleases(ctx)
		data.ComparedCommitCount, _ = strconv.Atoi(h.getUpdateSetting(ctx, "update_comparison_commit_count", "0"))
		if chk.Commit != "" && h.buildCommit != "" && h.buildCommit != "unknown" {
			data.ComparisonURL = "https://github.com/" + updateRepository + "/compare/" + url.PathEscape(h.buildCommit) + "..." + url.PathEscape(updateBranch())
		}
	}
	if checked := h.getUpdateSetting(ctx, "update_last_checked_at", ""); checked != "" {
		if parsed, err := time.Parse(time.RFC3339, checked); err == nil {
			data.LastChecked = parsed.Local().Format("02.01.2006 15:04:05")
		}
	}
	if data.AgentConfigured {
		if status, err := h.fetchUpdateAgentStatus(ctx); err == nil {
			data.AgentReachable = true
			data.AgentOutput = status.Output
			if status.Running {
				data.AgentStatus = "Update läuft seit " + status.StartedAt.Local().Format("15:04:05")
			} else if !status.FinishedAt.IsZero() {
				if status.Success {
					data.AgentStatus = "Letztes Update erfolgreich beendet"
				} else {
					data.AgentStatus = "Letzter Updateversuch fehlgeschlagen"
				}
			}
		} else {
			data.AgentStatus = "Update-Agent nicht erreichbar"
		}
	}
	if data.AgentConfigured {
		if st, cluster, err := h.updateCluster(ctx); err == nil && cluster {
			data.UpdateCluster = true
			data.UpdatePassive = st.Self.Role != "aktiv" || h.Mirror().On
			data.UpdateKindText = updateKindText(st.Window.Kind)
		}
	}
	// Rückfrage nur aus dem Serverzustand dieses Benutzers; die Adresszeile trägt keinen Text
	if r.URL.Query().Get("ask") == "1" {
		if a, ok := updateAskGet(getUser(r).ID); ok {
			data.UpdateAsk = tr(h.requestLang(r), "Nicht alles grün (%s). Trotzdem mit kurzer Unterbrechung aktualisieren?", a.Reason)
			data.UpdateAskHints, data.UpdateAskToken = a.Hints, a.Token
		}
	}
	data.NC = h.ncUpdateViewFor(r.Context())
	data.CanCode = h.actorIsAdmin(r)
	data.CanPKI = h.canPKI(r)
	if data.CanCode {
		data.AdminTools = enabledAdminTools()
	}
	data.InfraRespDefault = h.getUpdateSetting(r.Context(), infraRespDefaultSetting, "1") == "1"
	h.render(w, "core_settings", data)
}

// infraRespDefaultSetting: liest der Datenbank-Trigger pdh_infra_responsible_default
// (migrations/129) – „1“ bzw. nicht gesetzt = an, „0“ = aus.
const infraRespDefaultSetting = "infra_responsible_default"

// InfraRespDefaultWeb: POST /core/settings/infra-responsible (on=1|leer)
func (h *Handler) InfraRespDefaultWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	v, msg := "0", "Verantwortliche werden nicht mehr aus der Infrastruktur übernommen"
	if r.FormValue("on") == "1" {
		v, msg = "1", "Neue Vorgänge übernehmen die Verantwortlichen der Anlage"
	}
	if err := h.setUpdateSetting(r.Context(), infraRespDefaultSetting, v); err != nil {
		msg = "Einstellung konnte nicht gespeichert werden: " + err.Error()
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (h *Handler) SaveCoreSettings(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Einstellungen konnten nicht gelesen werden", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("auto_check_enabled") == "on"
	interval, err := strconv.Atoi(r.FormValue("check_interval_hours"))
	if err != nil || interval < 1 || interval > 168 {
		http.Redirect(w, r, "/core/settings?notice=Prüfintervall muss zwischen 1 und 168 Stunden liegen", http.StatusSeeOther)
		return
	}
	if err := h.setUpdateSetting(r.Context(), "update_auto_check_enabled", strconv.FormatBool(enabled)); err != nil {
		http.Error(w, "Einstellungen konnten nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	if err := h.setUpdateSetting(r.Context(), "update_check_interval_hours", strconv.Itoa(interval)); err != nil {
		http.Error(w, "Einstellungen konnten nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/core/settings?notice=Einstellungen+gespeichert", http.StatusSeeOther)
}

// SaveDueDateSettings speichert die vier Standard-Fristen (Tage nach
// Anlage), die greifen, wenn beim Anlegen einer Störung/eines Tickets/
// einer Aufgabe/einer Wartung kein eigener Termin angegeben wird.
func (h *Handler) SaveDueDateSettings(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Einstellungen konnten nicht gelesen werden", http.StatusBadRequest)
		return
	}
	fields := []struct {
		formName string
		key      string
	}{
		{"due_days_fault", appsettings.KeyDefaultDueDaysFault},
		{"due_days_ticket", appsettings.KeyDefaultDueDaysTicket},
		{"due_days_task", appsettings.KeyDefaultDueDaysTask},
		{"due_days_maintenance", appsettings.KeyDefaultDueDaysMaintenance},
	}
	values := make(map[string]int, len(fields))
	for _, f := range fields {
		days, err := strconv.Atoi(r.FormValue(f.formName))
		if err != nil || days < 1 || days > 365 {
			http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape("Standard-Fristen müssen zwischen 1 und 365 Tagen liegen"), http.StatusSeeOther)
			return
		}
		values[f.key] = days
	}
	for _, f := range fields {
		if err := h.setUpdateSetting(r.Context(), f.key, strconv.Itoa(values[f.key])); err != nil {
			http.Error(w, "Einstellungen konnten nicht gespeichert werden", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/core/settings?notice=Standard-Fristen+gespeichert", http.StatusSeeOther)
}

func (h *Handler) CheckUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if err := h.checkGitHubUpdate(r.Context()); err != nil {
		http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape("GitHub-Prüfung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	if h.buildCommit == "" || h.buildCommit == "unknown" {
		http.Redirect(w, r, "/core/settings?notice=Installierter+Commit+unbekannt%3B+Update-Status+nicht+vergleichbar", http.StatusSeeOther)
		return
	}
	switch h.getUpdateSetting(r.Context(), "update_comparison_status", "") {
	case "identical":
		http.Redirect(w, r, "/core/settings?notice=PDH+ist+auf+dem+aktuellen+Stand", http.StatusSeeOther)
	case "ahead":
		http.Redirect(w, r, "/core/settings?notice=Ein+Update+ist+verfügbar", http.StatusSeeOther)
	case "behind":
		http.Redirect(w, r, "/core/settings?notice=Der+installierte+Stand+liegt+vor+GitHub+main", http.StatusSeeOther)
	case "diverged":
		http.Redirect(w, r, "/core/settings?notice=Lokaler+Stand+ist+abweichend%3B+Fast-Forward+nicht+möglich", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/core/settings?notice=Commit-Vergleich+nicht+verfügbar", http.StatusSeeOther)
	}
}

// installUpdateLegacy: Update wie bisher (Docker-Betrieb, ältere Agenten):
// der Agent holt und installiert den Stand sofort (POST /v1/update).
// Rechte und Agent-Einstellung hat InstallUpdateWeb schon geprüft.
func (h *Handler) installUpdateLegacy(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.updateAgentURL+"/v1/update", nil)
	if err != nil {
		http.Redirect(w, r, "/core/settings?notice=Update-Agent-Adresse+ungültig", http.StatusSeeOther)
		return
	}
	req.Header.Set("Authorization", "Bearer "+h.updateAgentToken)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Redirect(w, r, "/core/settings?notice=Update-Agent+nicht+erreichbar", http.StatusSeeOther)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape("Update wurde gestartet. Beim Neustart sichert das PDH vor den Datenbank-Änderungen automatisch den kompletten bisherigen Stand (Datensicherung → Protokoll)."), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/core/settings?notice="+url.QueryEscape(fmt.Sprintf("Update-Agent meldet HTTP %d", resp.StatusCode)), http.StatusSeeOther)
}

func (h *Handler) getUpdateSetting(ctx context.Context, key, fallback string) string {
	var value string
	if err := h.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&value); err != nil {
		return fallback
	}
	return value
}

func (h *Handler) setUpdateSetting(ctx context.Context, key, value string) error {
	_, err := h.db.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=NOW()`, key, value)
	return err
}

func (h *Handler) checkUpdateIfDue(ctx context.Context) {
	enabled := h.getUpdateSetting(ctx, "update_auto_check_enabled", "true") == "true"
	interval, err := strconv.Atoi(h.getUpdateSetting(ctx, "update_check_interval_hours", "24"))
	if err != nil || interval < 1 || interval > 168 {
		interval = 24
	}
	last, _ := time.Parse(time.RFC3339, h.getUpdateSetting(ctx, "update_last_checked_at", ""))
	stale := !h.loadUpdateCheck(ctx).current(h.buildCommit, updateBranch())
	if !updateCheckDue(enabled, stale, last, interval, time.Now()) {
		return
	}
	if err := h.checkGitHubUpdate(ctx); err != nil {
		fmt.Printf("update check failed: %v\n", err)
	}
}

// updateCheckDue: automatische Prüfung jetzt fällig? Nach einem Update (anderer
// Stand) oder Zweigwechsel sofort, sonst nach dem Intervall; ausgeschaltet nie.
func updateCheckDue(enabled, stale bool, last time.Time, intervalHours int, now time.Time) bool {
	if !enabled {
		return false
	}
	return stale || last.IsZero() || now.Sub(last) >= time.Duration(intervalHours)*time.Hour
}

// storedUpdateCheck: letzter Commit-Vergleich, wie checkGitHubUpdate ihn
// gespeichert hat – mit dem Stand und Update-Zweig, für die er galt (A1).
type storedUpdateCheck struct {
	Commit string // neuester Stand des Update-Zweigs (voll)
	Status string // ahead, behind, identical, diverged; "" = kein Vergleich
	Build  string // laufender Stand bei der Prüfung ("unknown" = unbekannt)
	Branch string // Update-Zweig bei der Prüfung
}

// checkedBuild: so wird der laufende Stand beim Vergleich gespeichert.
func checkedBuild(build string) string {
	if build = strings.TrimSpace(build); build == "" {
		return "unknown"
	}
	return build
}

// current: Gehört der Vergleich zum laufenden Stand und Update-Zweig? Fehlen
// die Angaben (Vergleich einer älteren Version), gilt er als veraltet.
func (c storedUpdateCheck) current(build, branch string) bool {
	return c.Build != "" && c.Build == checkedBuild(build) && c.Branch != "" && c.Branch == branch
}

// target: Ziel des Update-Knopfs – nur aus einem aktuellen Vergleich, der
// einen neueren Stand gefunden hat; sonst "".
func (c storedUpdateCheck) target(build, branch string) string {
	if !c.current(build, branch) || !updateAvailable(c.Status, build) {
		return ""
	}
	return strings.TrimSpace(c.Commit)
}

// loadUpdateCheck liest den gespeicherten Vergleich (Tests: updateCheckHook).
func (h *Handler) loadUpdateCheck(ctx context.Context) storedUpdateCheck {
	if h.updateCheckHook != nil {
		return h.updateCheckHook(ctx)
	}
	if h.db == nil {
		return storedUpdateCheck{}
	}
	get := func(k string) string { return strings.TrimSpace(h.getUpdateSetting(ctx, k, "")) }
	return storedUpdateCheck{Commit: get("update_latest_commit"), Status: get("update_comparison_status"),
		Build: get("update_checked_build"), Branch: get("update_checked_branch")}
}

// applyUpdateCheck: Vergleich für die Seite. Veraltet: kein Update anbieten
// und den alten Vergleich nicht als aktuell zeigen.
func (d *CoreSettingsPageData) applyUpdateCheck(c storedUpdateCheck, build, branch string) {
	if !c.current(build, branch) {
		d.UpdateCheckStale = true
		d.LatestCommit, d.ComparisonStatus, d.UpdateAvailable = "", "", false
		return
	}
	d.LatestCommit = shortCommit(c.Commit)
	d.ComparisonStatus = c.Status
	d.UpdateAvailable = updateAvailable(c.Status, build)
}

func (h *Handler) checkGitHubUpdate(ctx context.Context) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var latest struct {
		SHA string `json:"sha"`
	}
	if err := fetchGitHubJSON(requestCtx, "https://api.github.com/repos/"+updateRepository+"/commits/"+url.PathEscape(updateBranch()), &latest); err != nil {
		h.recordUpdateCheckError(ctx, err)
		return err
	}
	latestCommit := latest.SHA
	if latestCommit == "" {
		err := fmt.Errorf("GitHub-Antwort enthält keinen Commit")
		h.recordUpdateCheckError(ctx, err)
		return err
	}
	comparisonStatus := ""
	var result githubComparisonResponse
	if h.buildCommit != "" && h.buildCommit != "unknown" {
		endpoint := "https://api.github.com/repos/" + updateRepository + "/compare/" + url.PathEscape(h.buildCommit) + "..." + url.PathEscape(latestCommit)
		if err := fetchGitHubJSON(requestCtx, endpoint, &result); err != nil {
			h.recordUpdateCheckError(ctx, err)
			return err
		}
		comparisonStatus = result.Status
		switch comparisonStatus {
		case "ahead", "behind", "identical", "diverged":
		default:
			err := fmt.Errorf("GitHub-Antwort enthält keinen gültigen Commit-Vergleich")
			h.recordUpdateCheckError(ctx, err)
			return err
		}
	}
	if err := h.setUpdateSetting(ctx, "update_latest_commit", latestCommit); err != nil {
		return err
	}
	if err := h.setUpdateSetting(ctx, "update_comparison_status", comparisonStatus); err != nil {
		return err
	}
	// A1: Stand und Zweig dieses Vergleichs – nach einem Update oder
	// Zweigwechsel gilt er als veraltet
	if err := h.setUpdateSetting(ctx, "update_checked_build", checkedBuild(h.buildCommit)); err != nil {
		return err
	}
	if err := h.setUpdateSetting(ctx, "update_checked_branch", updateBranch()); err != nil {
		return err
	}
	commitViews := make([]UpdateCommitView, 0)
	if len(result.Commits) > 0 {
		start := len(result.Commits) - 20
		if start < 0 {
			start = 0
		}
		for index := len(result.Commits) - 1; index >= start; index-- {
			commit := result.Commits[index]
			message := strings.SplitN(strings.TrimSpace(commit.Commit.Message), "\n", 2)[0]
			if len([]rune(message)) > 180 {
				message = string([]rune(message)[:177]) + "..."
			}
			commitURL := commit.HTMLURL
			if commitURL == "" {
				commitURL = "https://github.com/" + updateRepository + "/commit/" + url.PathEscape(commit.SHA)
			}
			commitViews = append(commitViews, UpdateCommitView{
				SHA:     shortCommit(commit.SHA),
				URL:     commitURL,
				Message: message,
				Date:    commit.Commit.Author.Date.Local().Format("02.01.2006 15:04"),
			})
		}
	}
	commitJSON, err := json.Marshal(commitViews)
	if err != nil {
		return err
	}
	if err := h.setUpdateSetting(ctx, "update_comparison_commits", string(commitJSON)); err != nil {
		return err
	}
	commitCount := result.AheadBy + result.BehindBy
	if err := h.setUpdateSetting(ctx, "update_comparison_commit_count", strconv.Itoa(commitCount)); err != nil {
		return err
	}
	h.checkGitHubVersion(requestCtx)
	_ = h.setUpdateSetting(ctx, "update_last_check_error", "")
	return h.setUpdateSetting(ctx, "update_last_checked_at", time.Now().UTC().Format(time.RFC3339))
}

func fetchGitHubJSON(ctx context.Context, endpoint string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "pdh-server-update-checker")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub antwortet mit HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (h *Handler) recordUpdateCheckError(ctx context.Context, err error) {
	_ = h.setUpdateSetting(ctx, "update_comparison_status", "")
	_ = h.setUpdateSetting(ctx, "update_comparison_commits", "[]")
	_ = h.setUpdateSetting(ctx, "update_comparison_commit_count", "0")
	_ = h.setUpdateSetting(ctx, "update_last_check_error", err.Error())
	_ = h.setUpdateSetting(ctx, "update_last_checked_at", time.Now().UTC().Format(time.RFC3339))
}

func (h *Handler) fetchUpdateAgentStatus(ctx context.Context) (updateAgentStatus, error) {
	var status updateAgentStatus
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.updateAgentURL+"/v1/status", nil)
	if err != nil {
		return status, err
	}
	req.Header.Set("Authorization", "Bearer "+h.updateAgentToken)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return status, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return status, fmt.Errorf("Update-Agent HTTP %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&status)
	return status, err
}

func shortCommit(commit string) string {
	if len(commit) > 10 {
		return commit[:10]
	}
	return commit
}

func updateAvailable(comparisonStatus, buildCommit string) bool {
	return comparisonStatus == "ahead" && buildCommit != "" && buildCommit != "unknown"
}
