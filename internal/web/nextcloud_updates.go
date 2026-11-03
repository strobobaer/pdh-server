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
)

// Core-Einstellungen → Kachel „Nextcloud-Updates“: Nextcloud auf demselben
// Server prüfen und mit einem Klick alle Apps bzw. das System aktualisieren.
// Ausgeführt vom Update-Agenten (cmd/update-agent/nextcloud.go).

const (
	keyNCDir       = "nextcloud.dir"
	keyNCLastCheck = "nextcloud.update.last"
)

type ncCheckResult struct {
	Version      string   `json:"version"`
	ServerUpdate string   `json:"server_update"` // z. B. "Nextcloud 29.0.5"
	AppUpdates   []string `json:"app_updates"`   // "files_pdfviewer → 3.0.1"
	UpToDate     bool     `json:"up_to_date"`
	CheckedAt    string   `json:"checked_at"`
	Error        string   `json:"error"`
	Output       string   `json:"output"`
}

type ncJobView struct {
	Running, Success, Done bool
	Output, Finished       string
}

type ncUpdateView struct {
	Dir         string
	Check       *ncCheckResult
	Job         *ncJobView
	AgentReady  bool
	UpdateCount int
}

var (
	ncServerUpdateRe = regexp.MustCompile(`(?m)^(Nextcloud [0-9][0-9.]*\S*) is available`)
	ncAppUpdateRe    = regexp.MustCompile(`(?m)^Update for (\S+) to version (\S+) is available`)
	ncVersionRe      = regexp.MustCompile(`"versionstring"\s*:\s*"([^"]+)"`)
)

// parseNCCheck: Ausgabe von occ status/update:check auswerten.
func parseNCCheck(out string) ncCheckResult {
	res := ncCheckResult{Output: out}
	if m := ncVersionRe.FindStringSubmatch(out); m != nil {
		res.Version = m[1]
	}
	if m := ncServerUpdateRe.FindStringSubmatch(out); m != nil {
		res.ServerUpdate = m[1]
	}
	for _, m := range ncAppUpdateRe.FindAllStringSubmatch(out, -1) {
		res.AppUpdates = append(res.AppUpdates, m[1]+" → "+m[2])
	}
	res.UpToDate = res.ServerUpdate == "" && len(res.AppUpdates) == 0 && strings.Contains(out, "### update:check")
	return res
}

func (h *Handler) ncDir(ctx context.Context) string {
	return h.getUpdateSetting(ctx, keyNCDir, "/var/www/nextcloud")
}

// agentRaw: Anfrage an den Update-Agenten; Statuscode und Rumpf unverändert.
func (h *Handler) agentRaw(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if !h.driveAgentConfigured() {
		return 0, nil, errors.New("Der Update-Agent ist nicht eingerichtet")
	}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, h.updateAgentURL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.updateAgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, errors.New("Update-Agent nicht erreichbar: " + err.Error())
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return resp.StatusCode, data, nil
}

// agentJSON: Anfrage an den Update-Agenten.
func (h *Handler) agentJSON(ctx context.Context, method, path string, body, out any) error {
	code, data, err := h.agentRaw(ctx, method, path, body)
	if err != nil {
		return err
	}
	status := fmt.Sprintf("%d %s", code, http.StatusText(code))
	if code == http.StatusNotFound {
		return errors.New("Der Update-Agent ist zu alt – zuerst das PDH aktualisieren")
	}
	var base struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &base) != nil {
		return fmt.Errorf("Update-Agent antwortet mit %s", status)
	}
	if out != nil {
		_ = json.Unmarshal(data, out)
	}
	if !base.OK {
		if base.Error == "" {
			base.Error = status
		}
		return errors.New(base.Error)
	}
	return nil
}

// ncUpdateViewFor: Daten der Kachel (letzte Prüfung, laufender Job).
func (h *Handler) ncUpdateViewFor(ctx context.Context) *ncUpdateView {
	v := &ncUpdateView{Dir: h.ncDir(ctx), AgentReady: h.driveAgentConfigured()}
	if raw := h.getUpdateSetting(ctx, keyNCLastCheck, ""); raw != "" {
		var c ncCheckResult
		if json.Unmarshal([]byte(raw), &c) == nil {
			v.Check = &c
			v.UpdateCount = len(c.AppUpdates)
			if c.ServerUpdate != "" {
				v.UpdateCount++
			}
		}
	}
	if v.AgentReady {
		ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		var st struct {
			Status struct {
				Running    bool      `json:"running"`
				Success    bool      `json:"success"`
				Output     string    `json:"output"`
				FinishedAt time.Time `json:"finished_at"`
			} `json:"status"`
		}
		if h.agentJSON(ctx, http.MethodGet, "/v1/nextcloud/status", nil, &st) == nil && (st.Status.Running || st.Status.Output != "") {
			j := &ncJobView{Running: st.Status.Running, Success: st.Status.Success, Output: st.Status.Output, Done: !st.Status.Running}
			if !st.Status.FinishedAt.IsZero() && st.Status.FinishedAt.Year() > 2000 {
				j.Finished = st.Status.FinishedAt.Local().Format("02.01.2006 15:04")
			}
			v.Job = j
		}
	}
	return v
}

func ncBack(w http.ResponseWriter, r *http.Request, msg string, err error) {
	q := url.Values{}
	if err != nil {
		q.Set("notice", "Nextcloud: "+err.Error())
	} else {
		q.Set("notice", msg)
	}
	http.Redirect(w, r, "/core/settings?"+q.Encode()+"#nextcloud-updates", http.StatusSeeOther)
}

// NextcloudCheckWeb: POST /core/settings/nextcloud/check
func (h *Handler) NextcloudCheckWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	var out struct {
		Output string `json:"output"`
	}
	err := h.agentJSON(ctx, http.MethodPost, "/v1/nextcloud/check", map[string]string{"dir": h.ncDir(ctx)}, &out)
	res := parseNCCheck(out.Output)
	res.CheckedAt = time.Now().Format("02.01.2006 15:04")
	if err != nil {
		res.Error = err.Error()
	}
	raw, _ := json.Marshal(res)
	_ = h.setUpdateSetting(r.Context(), keyNCLastCheck, string(raw))
	switch {
	case err != nil:
		ncBack(w, r, "", err)
	case res.UpToDate:
		ncBack(w, r, "Nextcloud "+res.Version+" ist aktuell – System und Apps.", nil)
	default:
		n := len(res.AppUpdates)
		msg := fmt.Sprintf("Nextcloud %s: %d App-Update(s) verfügbar", res.Version, n)
		if res.ServerUpdate != "" {
			msg += ", System-Update auf " + res.ServerUpdate
		}
		ncBack(w, r, msg+".", nil)
	}
}

// NextcloudUpdateWeb: POST /core/settings/nextcloud/update (what=apps|system|all)
func (h *Handler) NextcloudUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	what := r.FormValue("what")
	label := map[string]string{"apps": "Update aller Apps", "system": "System-Update", "all": "System- und App-Update"}[what]
	if label == "" {
		ncBack(w, r, "", errors.New("unbekannte Aktion"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := h.agentJSON(ctx, http.MethodPost, "/v1/nextcloud/update", map[string]string{"dir": h.ncDir(ctx), "what": what}, nil); err != nil {
		ncBack(w, r, "", err)
		return
	}
	componentLog("nextcloud").Info().Str("aktion", what).Str("user", getUser(r).ID).Msg("nextcloud-update gestartet")
	h.notifyAdmins(r.Context(), "☁️ **Nextcloud: "+label+" gestartet** von "+h.chatUserName(r.Context(), getUser(r).ID))
	ncBack(w, r, "Nextcloud: "+label+" gestartet – der Fortschritt steht in der Kachel (Seite neu laden). Nextcloud ist dabei kurz im Wartungsmodus.", nil)
}

// NextcloudDirWeb: POST /core/settings/nextcloud/dir
func (h *Handler) NextcloudDirWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	dir := strings.TrimRight(strings.TrimSpace(r.FormValue("dir")), "/")
	if !regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,200}$`).MatchString(dir) || strings.Contains(dir, "..") {
		ncBack(w, r, "", errors.New("Verzeichnis bitte als absoluten Pfad angeben, z. B. /var/www/nextcloud"))
		return
	}
	ncBack(w, r, "Nextcloud-Verzeichnis gespeichert: "+dir, h.setUpdateSetting(r.Context(), keyNCDir, dir))
}
