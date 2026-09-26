package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const updateRepository = "strobobaer/pdh-server"

type CoreSettingsPageData struct {
	BaseData
	AutoCheckEnabled    bool
	CheckInterval       int
	CurrentCommit       string
	LatestCommit        string
	ComparisonURL       string
	ComparisonStatus    string
	ComparedCommitCount int
	ComparisonCommits   []UpdateCommitView
	LastChecked         string
	LastCheckError      string
	UpdateAvailable     bool
	AgentConfigured     bool
	AgentReachable      bool
	AgentStatus         string
	AgentOutput         string
	Notice              string
}

type UpdateCommitView struct {
	SHA     string
	URL     string
	Message string
	Date    string
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
		h.checkUpdateIfDue(ctx)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
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
		interval = 24
	}
	latestCommit := h.getUpdateSetting(ctx, "update_latest_commit", "")
	data := CoreSettingsPageData{
		BaseData:         h.baseData(r, "core-settings", "Core-Einstellungen", "Systemverwaltung"),
		AutoCheckEnabled: enabled,
		CheckInterval:    interval,
		CurrentCommit:    shortCommit(h.buildCommit),
		LatestCommit:     shortCommit(latestCommit),
		ComparisonStatus: h.getUpdateSetting(ctx, "update_comparison_status", ""),
		LastCheckError:   h.getUpdateSetting(ctx, "update_last_check_error", ""),
		AgentConfigured:  h.updateAgentURL != "" && h.updateAgentToken != "",
		Notice:           r.URL.Query().Get("notice"),
	}
	_ = json.Unmarshal([]byte(h.getUpdateSetting(ctx, "update_comparison_commits", "[]")), &data.ComparisonCommits)
	data.ComparedCommitCount, _ = strconv.Atoi(h.getUpdateSetting(ctx, "update_comparison_commit_count", "0"))
	if latestCommit != "" && h.buildCommit != "" && h.buildCommit != "unknown" {
		data.ComparisonURL = "https://github.com/" + updateRepository + "/compare/" + url.PathEscape(h.buildCommit) + "...main"
	}
	if checked := h.getUpdateSetting(ctx, "update_last_checked_at", ""); checked != "" {
		if parsed, err := time.Parse(time.RFC3339, checked); err == nil {
			data.LastChecked = parsed.Local().Format("02.01.2006 15:04:05")
		}
	}
	data.UpdateAvailable = updateAvailable(data.ComparisonStatus, h.buildCommit)
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
	h.render(w, "core_settings", data)
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

func (h *Handler) InstallUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if h.updateAgentURL == "" || h.updateAgentToken == "" {
		http.Redirect(w, r, "/core/settings?notice=Update-Agent+nicht+konfiguriert", http.StatusSeeOther)
		return
		var result githubComparisonResponse
		http.Redirect(w, r, "/core/settings?notice=Update+wurde+gestartet", http.StatusSeeOther)
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
	if h.getUpdateSetting(ctx, "update_auto_check_enabled", "true") != "true" {
		return
	}
	interval, err := strconv.Atoi(h.getUpdateSetting(ctx, "update_check_interval_hours", "24"))
	if err != nil || interval < 1 || interval > 168 {
		interval = 24
	}
	last, err := time.Parse(time.RFC3339, h.getUpdateSetting(ctx, "update_last_checked_at", ""))
	if err == nil && time.Since(last) < time.Duration(interval)*time.Hour {
		return
	}
	if err := h.checkGitHubUpdate(ctx); err != nil {
		fmt.Printf("update check failed: %v\n", err)
	}
}

func (h *Handler) checkGitHubUpdate(ctx context.Context) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var latest struct {
		SHA string `json:"sha"`
	}
	if err := fetchGitHubJSON(requestCtx, "https://api.github.com/repos/"+updateRepository+"/commits/main", &latest); err != nil {
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
	var result struct {
		Status   string `json:"status"`
		AheadBy  int    `json:"ahead_by"`
		BehindBy int    `json:"behind_by"`
		Commits  []struct {
			SHA     string `json:"sha"`
			HTMLURL string `json:"html_url"`
			Commit  struct {
				Message string `json:"message"`
				Author  struct {
					Date time.Time `json:"date"`
				} `json:"author"`
			} `json:"commit"`
		} `json:"commits"`
	}
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