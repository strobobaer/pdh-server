package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"pdh/pkg/config"
)

// Server-Einstellungen: Werte aus der Docker-/systemd-Umgebung (.env.docker)
// in die Datenbank übernehmen, je Bereich testen und – wenn gesichert und grün –
// aus .env.docker entfernen (Update-Agent, cmd/update-agent/envfile.go).

// envTests: Bereich → Test (?what=) und ob das Ergebnis gespeichert wird.
var envTests = map[string]struct {
	What   string
	Record bool
}{
	"database":   {"database", true},
	"mail":       {"mail", true},
	"microsoft":  {"microsoft", true},
	"nextcloud":  {"nextcloud", true},
	"translate":  {"deepl", true},
	"copilot":    {"copilot", false},
	"cloudflare": {"cloudflared", false},
}

// envTestGroup: Bereich zu einem Test.
func envTestGroup(what string) string {
	for g, t := range envTests {
		if t.What == what {
			return g
		}
	}
	return ""
}

type envTestResult struct {
	OK      bool
	At, Msg string
}

func envTestKey(group string) string { return "envtest." + group }

func (h *Handler) recordEnvTest(ctx context.Context, group string, ok bool, msg string) {
	if group == "" || h.db == nil {
		return
	}
	state := "fail"
	if ok {
		state = "ok"
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	_ = h.setUpdateSetting(ctx, envTestKey(group), state+"|"+time.Now().Format(time.RFC3339)+"|"+msg)
}

func (h *Handler) envTestResult(ctx context.Context, group string) *envTestResult {
	raw := h.getUpdateSetting(ctx, envTestKey(group), "")
	parts := strings.SplitN(raw, "|", 3)
	if len(parts) != 3 {
		return nil
	}
	res := &envTestResult{OK: parts[0] == "ok", Msg: parts[2]}
	if t, err := time.Parse(time.RFC3339, parts[1]); err == nil {
		res.At = t.Local().Format("02.01.2006 15:04")
	}
	return res
}

// fillEnvTakeover: je Bereich, was aus der Umgebung kommt, was übernommen
// werden kann und was nach dem Übernehmen aus .env.docker entfallen kann.
func (h *Handler) fillEnvTakeover(ctx context.Context, d *ServerConfigData) {
	d.AgentReady = h.driveAgentConfigured()
	removed := h.envRemovedKeys(ctx)
	for i := range d.Groups {
		g := &d.Groups[i]
		if t, ok := envTests[g.Key]; ok {
			g.TestWhat, g.TestRecorded = t.What, t.Record
			if t.Record {
				g.Test = h.envTestResult(ctx, g.Key)
			}
		}
		for j := range g.Fields {
			f := &g.Fields[j]
			if !config.InProcessEnv(f.Key) {
				continue
			}
			switch {
			case config.TakeOverBlocked(f.Key):
				g.EnvStays = append(g.EnvStays, f.Key)
			case config.OverridesEnv(f.Key) && removed[f.Key]:
				// schon aus .env.docker entfernt – bis zum Neuaufbau des Containers noch in der Umgebung
			case config.OverridesEnv(f.Key):
				f.StillInEnv = true
				g.Removable = append(g.Removable, f.Key)
			default:
				g.TakeOver = append(g.TakeOver, f.Key)
				d.TakeOverTotal++
			}
		}
		// grün = gespeicherter Test erfolgreich (Bereiche ohne gespeicherten Test: übernommen genügt)
		g.CanRemove = len(g.Removable) > 0 && (!g.TestRecorded || g.Test != nil && g.Test.OK)
	}
}

// ServerConfigTakeOverWeb: POST /admin/server-config/takeover (group=<Bereich>|all)
func (h *Handler) ServerConfigTakeOverWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if h.db == nil {
		serverConfigRedirect(w, r, "", "", errors.New("keine Datenbank"))
		return
	}
	group := r.FormValue("group")
	var keys []string
	for _, g := range envGroups {
		if group != "all" && g.Key != group {
			continue
		}
		for _, f := range g.Fields {
			if config.FromProcessEnv(f.Key) && !config.TakeOverBlocked(f.Key) {
				keys = append(keys, f.Key)
			}
		}
	}
	tab := group
	if tab == "all" {
		tab = ""
	}
	done, err := config.TakeOverProcessEnv(r.Context(), h.db, keys, IsSecretSetting, getUser(r).ID)
	if err != nil {
		serverConfigRedirect(w, r, tab, "", err)
		return
	}
	if _, err := config.ApplyDBSettings(r.Context(), h.db); err != nil {
		serverConfigRedirect(w, r, tab, "", err)
		return
	}
	h.reloadLiveConfig(r.Context())
	if len(done) == 0 {
		serverConfigRedirect(w, r, tab, "Nichts zu übernehmen.", nil)
		return
	}
	componentLog("konfiguration").Info().Strs("keys", done).Str("user", getUser(r).ID).Msg("aus der umgebung in die datenbank übernommen")
	h.notifyAdmins(r.Context(), "⚙️ **Server-Einstellungen aus .env.docker in die Datenbank übernommen** von "+h.chatUserName(r.Context(), getUser(r).ID)+": "+strings.Join(done, ", "))
	serverConfigRedirect(w, r, tab, fmt.Sprintf("%d Wert(e) verschlüsselt in die Datenbank übernommen – sie gelten ab sofort aus der Datenbank. Jetzt testen; bei Grün können sie aus .env.docker entfernt werden.", len(done)), nil)
}

// ServerConfigEnvRemoveWeb: POST /admin/server-config/env-remove (group) –
// übernommene, getestete Werte in .env.docker auskommentieren (Update-Agent).
func (h *Handler) ServerConfigEnvRemoveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	d := &ServerConfigData{}
	for _, g := range envGroups {
		d.Groups = append(d.Groups, envGroupView{Key: g.Key, Label: g.Label, Fields: func() []envFieldView {
			var out []envFieldView
			for _, f := range g.Fields {
				out = append(out, envFieldView{envField: f})
			}
			return out
		}()})
	}
	h.fillEnvTakeover(r.Context(), d)
	group := r.FormValue("group")
	var keys []string
	for _, g := range d.Groups {
		if g.Key != group {
			continue
		}
		if !g.CanRemove {
			serverConfigRedirect(w, r, group, "", errors.New("Erst übernehmen und den Test grün bestehen – dann lässt sich der Eintrag entfernen"))
			return
		}
		keys = g.Removable
	}
	if len(keys) == 0 {
		serverConfigRedirect(w, r, group, "Nichts zu entfernen.", nil)
		return
	}
	removed, err := h.agentEnvRemove(r.Context(), keys)
	if err != nil {
		serverConfigRedirect(w, r, group, "", err)
		return
	}
	h.markEnvRemoved(r.Context(), removed)
	componentLog("konfiguration").Info().Strs("keys", removed).Str("user", getUser(r).ID).Msg(".env.docker bereinigt")
	serverConfigRedirect(w, r, group, fmt.Sprintf("Aus .env.docker entfernt: %s. Beim nächsten Update bzw. Neuaufbau des Containers verschwinden sie auch aus der Umgebung.", strings.Join(removed, ", ")), nil)
}

const keyEnvRemoved = "envdocker.removed"

// envRemovedKeys: bereits aus .env.docker entfernte Schlüssel.
func (h *Handler) envRemovedKeys(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, k := range strings.Split(h.getUpdateSetting(ctx, keyEnvRemoved, ""), ",") {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = true
		}
	}
	return out
}

func (h *Handler) markEnvRemoved(ctx context.Context, keys []string) {
	set := h.envRemovedKeys(ctx)
	for _, k := range keys {
		set[k] = true
	}
	list := make([]string, 0, len(set))
	for k := range set {
		list = append(list, k)
	}
	sort.Strings(list)
	_ = h.setUpdateSetting(ctx, keyEnvRemoved, strings.Join(list, ","))
}

func (h *Handler) agentEnvRemove(ctx context.Context, keys []string) ([]string, error) {
	if !h.driveAgentConfigured() {
		return nil, errors.New("Der Update-Agent ist nicht eingerichtet – die Zeilen bitte von Hand aus .env.docker löschen")
	}
	raw, _ := json.Marshal(map[string]any{"keys": keys})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.updateAgentURL+"/v1/env/remove", strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.updateAgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New("Update-Agent nicht erreichbar: " + err.Error())
	}
	defer resp.Body.Close()
	var res struct {
		OK      bool     `json:"ok"`
		Error   string   `json:"error"`
		Removed []string `json:"removed"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if json.Unmarshal(data, &res) != nil {
		if resp.StatusCode == http.StatusNotFound {
			return nil, errors.New("Der Update-Agent ist zu alt – bitte PDH aktualisieren")
		}
		return nil, fmt.Errorf("Update-Agent antwortet mit %s", resp.Status)
	}
	if !res.OK {
		return nil, errors.New(res.Error)
	}
	if len(res.Removed) == 0 {
		return nil, errors.New("In .env.docker stand keiner der Werte (mehr) – bitte Datei prüfen")
	}
	sort.Strings(res.Removed)
	return res.Removed, nil
}

// ── Tests Microsoft 365, Nextcloud, DeepL ─────────────────────

// effectiveValue: Wert aus dem Formular (geändert, noch nicht gespeichert) oder der laufende.
func effectiveValue(updates map[string]string, key string) string {
	if v, ok := updates[key]; ok && v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(key))
}

var envTestClient = &http.Client{Timeout: 15 * time.Second}

// testMicrosoft: Anmeldung der App (Client-Credentials) beim Microsoft-Tenant.
func testMicrosoft(ctx context.Context, vals map[string]string) (string, error) {
	tenant, id, secret := effectiveValue(vals, "PDH_MICROSOFT_TENANT_ID"), effectiveValue(vals, "PDH_MICROSOFT_CLIENT_ID"), effectiveValue(vals, "PDH_MICROSOFT_CLIENT_SECRET")
	if tenant == "" || id == "" || secret == "" {
		return "", errors.New("Tenant-ID, Client-ID und Client-Secret eintragen")
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}, "scope": {"https://graph.microsoft.com/.default"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://login.microsoftonline.com/"+url.PathEscape(tenant)+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := envTestClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Microsoft nicht erreichbar: %v", err)
	}
	defer resp.Body.Close()
	var res struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&res)
	if resp.StatusCode != http.StatusOK || res.AccessToken == "" {
		msg := res.Description
		if i := strings.Index(msg, "\r\n"); i > 0 {
			msg = msg[:i]
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("Anmeldung abgelehnt (%s): %s", res.Error, msg)
	}
	return "Anmeldung bei Microsoft 365 erfolgreich (Tenant " + tenant + ").", nil
}

// testNextcloud: Anmeldung mit Benutzer und App-Passwort (OCS „cloud/user“).
func testNextcloud(ctx context.Context, vals map[string]string) (string, error) {
	base, user, pw := strings.TrimRight(effectiveValue(vals, "PDH_NEXTCLOUD_BASEURL"), "/"), effectiveValue(vals, "PDH_NEXTCLOUD_USERNAME"), effectiveValue(vals, "PDH_NEXTCLOUD_PASSWORD")
	if base == "" || user == "" || pw == "" {
		return "", errors.New("Adresse, Benutzer und App-Passwort eintragen")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/ocs/v1.php/cloud/user?format=json", nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, pw)
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	resp, err := envTestClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Nextcloud nicht erreichbar: %v", err)
	}
	defer resp.Body.Close()
	var res struct {
		OCS struct {
			Meta struct {
				StatusCode int `json:"statuscode"`
			} `json:"meta"`
			Data struct {
				ID          string `json:"id"`
				DisplayName string `json:"display-name"`
			} `json:"data"`
		} `json:"ocs"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&res)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", errors.New("Benutzer oder App-Passwort falsch")
	}
	if resp.StatusCode != http.StatusOK || res.OCS.Data.ID == "" {
		return "", fmt.Errorf("Nextcloud antwortet mit %s", resp.Status)
	}
	return "Angemeldet bei Nextcloud als " + res.OCS.Data.ID + ".", nil
}

// testDeepL: Schlüssel prüfen und Kontingent anzeigen.
func testDeepL(ctx context.Context, vals map[string]string) (string, error) {
	key := effectiveValue(vals, "PDH_DEEPL_KEY")
	if key == "" {
		return "", errors.New("DeepL-API-Schlüssel eintragen")
	}
	endpoint := "https://api.deepl.com/v2/usage"
	if strings.HasSuffix(key, ":fx") {
		endpoint = "https://api-free.deepl.com/v2/usage"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+key)
	resp, err := envTestClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("DeepL nicht erreichbar: %v", err)
	}
	defer resp.Body.Close()
	var res struct {
		Count int64 `json:"character_count"`
		Limit int64 `json:"character_limit"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&res)
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("Schlüssel ungültig")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DeepL antwortet mit %s", resp.Status)
	}
	return fmt.Sprintf("Schlüssel gültig – %d von %d Zeichen diesen Monat verbraucht.", res.Count, res.Limit), nil
}
