package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"pdh/internal/integrations/nextcloud"
)

type MicrosoftAdminPageData struct {
	BaseData
	Configured bool
	TenantID   string
	LastSync   string
	Matched    string
	Unmatched  string
	Notice     string
	Users      []MicrosoftDirectoryUserRow
	// Nextcloud-Konten beim Abgleich anlegen (NextcloudReady: Nextcloud eingerichtet)
	NextcloudProvision bool
	NextcloudReady     bool
}

// microsoftSyncResult: Zähler eines Verzeichnisabgleichs.
type microsoftSyncResult struct {
	Matched, Unmatched                int
	NextcloudCreated, NextcloudFailed int
}

// MicrosoftDirectoryUserRow ist ein aktiver PDH-Benutzer mit seiner
// (eventuell leeren) Microsoft-Verzeichniszuordnung - Grundlage fuer die
// manuelle Zuordnung, die den automatischen E-Mail-Abgleich ergaenzt
// (z.B. wenn PDH- und Microsoft-E-Mail-Adresse voneinander abweichen).
type MicrosoftDirectoryUserRow struct {
	PDHUserID       string
	PDHUserName     string
	PDHUserEmail    string
	MicrosoftUserID string
	MicrosoftEmail  string
	DisplayName     string
	Department      string
	Assigned        bool
}

type microsoftDirectoryUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
	Department        string `json:"department"`
	AccountEnabled    bool   `json:"accountEnabled"`
	// Rückfall für Signatur-Platzhalter (m365_signatures.go)
	GivenName      string   `json:"givenName"`
	Surname        string   `json:"surname"`
	JobTitle       string   `json:"jobTitle"`
	BusinessPhones []string `json:"businessPhones"`
	MobilePhone    string   `json:"mobilePhone"`
	OfficeLocation string   `json:"officeLocation"`
	CompanyName    string   `json:"companyName"`
}

func (u microsoftDirectoryUser) businessPhone() string {
	if len(u.BusinessPhones) > 0 {
		return u.BusinessPhones[0]
	}
	return ""
}

type microsoftDirectoryPage struct {
	Users    []microsoftDirectoryUser `json:"value"`
	NextLink string                   `json:"@odata.nextLink"`
}

func (h *Handler) MicrosoftAdminPage(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	data := MicrosoftAdminPageData{
		BaseData:   h.baseData(r, "core-settings", "Microsoft 365", "Verzeichnisverknüpfung"),
		TenantID:   h.microsoft.TenantID,
		Configured: h.microsoft.ClientID != "" && h.microsoft.ClientSecret != "" && h.microsoft.TenantID != "",
		LastSync:   h.getUpdateSetting(r.Context(), "microsoft_directory_last_sync", "Noch nicht synchronisiert"),
		Matched:    h.getUpdateSetting(r.Context(), "microsoft_directory_last_matched", "0"),
		Unmatched:  h.getUpdateSetting(r.Context(), "microsoft_directory_last_unmatched", "0"),
		Notice:     r.URL.Query().Get("notice"),

		NextcloudProvision: h.getUpdateSetting(r.Context(), microsoftNextcloudSetting, "") == "1",
		NextcloudReady:     nextcloud.UserClientFromEnv().Enabled(),
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT u.id::text, u.first_name || ' ' || u.last_name, u.email,
		       COALESCE(m.microsoft_user_id, ''), COALESCE(m.email, ''), COALESCE(m.display_name, ''), COALESCE(m.department, ''),
		       (m.pdh_user_id IS NOT NULL)
		FROM users u
		LEFT JOIN microsoft_directory_users m ON m.pdh_user_id = u.id AND m.tenant_id = $1
		WHERE u.active = true
		ORDER BY u.last_name, u.first_name`, h.microsoft.TenantID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var row MicrosoftDirectoryUserRow
			if rows.Scan(&row.PDHUserID, &row.PDHUserName, &row.PDHUserEmail,
				&row.MicrosoftUserID, &row.MicrosoftEmail, &row.DisplayName, &row.Department, &row.Assigned) == nil {
				data.Users = append(data.Users, row)
			}
		}
	}
	h.render(w, "microsoft_admin", data)
}

// MicrosoftDirectoryAssignWeb ordnet einem PDH-Benutzer manuell eine
// Microsoft-Verzeichnis-ID zu - Ergaenzung zum automatischen
// E-Mail-Abgleich, fuer Faelle, in denen die PDH- und die
// Microsoft-E-Mail-Adresse nicht uebereinstimmen. Es werden keine
// OAuth-Tokens ausgestellt; die Zuordnung dient nur dem Verzeichnis
// (z.B. Teams-Empfaenger-Auflösung), nicht dem persoenlichen
// Kalendersync, der weiterhin die eigene Verknuepfung unter "Mein Konto"
// voraussetzt.
func (h *Handler) MicrosoftDirectoryAssignWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if strings.TrimSpace(h.microsoft.TenantID) == "" {
		http.Redirect(w, r, "/core/settings/microsoft?notice="+url.QueryEscape("PDH_MICROSOFT_TENANT_ID ist nicht konfiguriert"), http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular konnte nicht gelesen werden", http.StatusBadRequest)
		return
	}
	pdhUserID := strings.TrimSpace(r.FormValue("pdh_user_id"))
	microsoftUserID := strings.TrimSpace(r.FormValue("microsoft_user_id"))
	email := strings.TrimSpace(r.FormValue("email"))
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	department := strings.TrimSpace(r.FormValue("department"))
	if pdhUserID == "" || microsoftUserID == "" {
		http.Redirect(w, r, "/core/settings/microsoft?notice="+url.QueryEscape("PDH-Benutzer und Microsoft-Objekt-ID sind Pflichtfelder"), http.StatusSeeOther)
		return
	}
	_, err := h.db.Exec(r.Context(), `
		INSERT INTO microsoft_directory_users (tenant_id, microsoft_user_id, pdh_user_id, email, display_name, department, account_enabled, synced_at)
		VALUES ($1, $2, $3::uuid, $4, $5, $6, true, NOW())
		ON CONFLICT (tenant_id, pdh_user_id) DO UPDATE SET
			microsoft_user_id=EXCLUDED.microsoft_user_id,
			email=EXCLUDED.email,
			display_name=EXCLUDED.display_name,
			department=EXCLUDED.department,
			account_enabled=true,
			synced_at=NOW()`,
		h.microsoft.TenantID, microsoftUserID, pdhUserID, email, displayName, department)
	if err != nil {
		http.Redirect(w, r, "/core/settings/microsoft?notice="+url.QueryEscape("Zuordnung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/core/settings/microsoft?notice=Zuordnung+gespeichert", http.StatusSeeOther)
}

func (h *Handler) MicrosoftDirectoryUnassignWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	pdhUserID := chi.URLParam(r, "id")
	_, err := h.db.Exec(r.Context(), `DELETE FROM microsoft_directory_users WHERE tenant_id=$1 AND pdh_user_id=$2::uuid`, h.microsoft.TenantID, pdhUserID)
	if err != nil {
		http.Redirect(w, r, "/core/settings/microsoft?notice="+url.QueryEscape("Zuordnung konnte nicht entfernt werden: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/core/settings/microsoft?notice=Zuordnung+entfernt", http.StatusSeeOther)
}

// MicrosoftNextcloudProvisionWeb schaltet das Anlegen von Nextcloud-Konten
// beim Abgleich ein oder aus.
func (h *Handler) MicrosoftNextcloudProvisionWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	value, notice := "0", "Nextcloud-Konten werden beim Abgleich nicht mehr angelegt"
	if r.FormValue("enabled") == "1" {
		value, notice = "1", "Nextcloud-Konten werden beim nächsten Abgleich angelegt"
	}
	if err := h.setUpdateSetting(r.Context(), microsoftNextcloudSetting, value); err != nil {
		notice = "Einstellung konnte nicht gespeichert werden: " + err.Error()
	}
	http.Redirect(w, r, "/core/settings/microsoft?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func (h *Handler) MicrosoftDirectorySyncWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canManageRoles(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	if h.microsoft.ClientID == "" || h.microsoft.ClientSecret == "" || h.microsoft.TenantID == "" {
		http.Redirect(w, r, "/core/settings/microsoft?notice=Microsoft-Verzeichnis+nicht+konfiguriert", http.StatusSeeOther)
		return
	}
	if !h.microsoftSyncMu.TryLock() {
		http.Redirect(w, r, "/core/settings/microsoft?notice=Synchronisierung+läuft+bereits", http.StatusSeeOther)
		return
	}
	defer h.microsoftSyncMu.Unlock()

	res, err := h.syncMicrosoftDirectory(r.Context(), getUser(r).ID)
	if err != nil {
		http.Redirect(w, r, "/core/settings/microsoft?notice="+url.QueryEscape("Synchronisierung fehlgeschlagen: "+err.Error()), http.StatusSeeOther)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = h.setUpdateSetting(r.Context(), "microsoft_directory_last_sync", now)
	_ = h.setUpdateSetting(r.Context(), "microsoft_directory_last_matched", fmt.Sprint(res.Matched))
	_ = h.setUpdateSetting(r.Context(), "microsoft_directory_last_unmatched", fmt.Sprint(res.Unmatched))
	notice := fmt.Sprintf("Synchronisierung abgeschlossen: %d zugeordnet, %d ohne passendes PDH-Konto", res.Matched, res.Unmatched)
	if res.NextcloudCreated > 0 || res.NextcloudFailed > 0 {
		notice += fmt.Sprintf(" · Nextcloud: %d Konten verknüpft", res.NextcloudCreated)
		if res.NextcloudFailed > 0 {
			notice += fmt.Sprintf(", %d nicht möglich (Server-Protokoll)", res.NextcloudFailed)
		}
	}
	http.Redirect(w, r, "/core/settings/microsoft?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

func (h *Handler) syncMicrosoftDirectory(ctx context.Context, actorID string) (microsoftSyncResult, error) {
	var res microsoftSyncResult
	accessToken, err := h.microsoftDirectoryToken(ctx)
	if err != nil {
		return res, err
	}
	provision := h.getUpdateSetting(ctx, microsoftNextcloudSetting, "") == "1" && nextcloud.UserClientFromEnv().Enabled()
	query := url.Values{
		"$select": {"id,displayName,mail,userPrincipalName,department,accountEnabled," + microsoftSignatureSelect},
		"$top":    {"999"},
	}
	nextURL := "https://graph.microsoft.com/v1.0/users?" + query.Encode()
	for nextURL != "" {
		var page microsoftDirectoryPage
		if err := h.getMicrosoftGraphJSON(ctx, accessToken, nextURL, &page); err != nil {
			return res, err
		}
		for _, directoryUser := range page.Users {
			email := strings.TrimSpace(directoryUser.Mail)
			if email == "" {
				email = strings.TrimSpace(directoryUser.UserPrincipalName)
			}
			if email == "" || directoryUser.ID == "" {
				res.Unmatched++
				continue
			}
			var pdhUserID string
			err := h.db.QueryRow(ctx, `
				SELECT id::text FROM users
				WHERE active=true AND lower(trim(email))=lower($1)
				LIMIT 1`, email).Scan(&pdhUserID)
			if errors.Is(err, pgx.ErrNoRows) {
				res.Unmatched++
				continue
			}
			if err != nil {
				return res, err
			}
			_, err = h.db.Exec(ctx, `
				INSERT INTO microsoft_directory_users
					(tenant_id, microsoft_user_id, pdh_user_id, email, display_name, department, account_enabled, synced_at,
					 given_name, surname, job_title, business_phone, mobile_phone, office_location, company_name)
				VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, NOW(), $8, $9, $10, $11, $12, $13, $14)
				ON CONFLICT (tenant_id, microsoft_user_id) DO UPDATE SET
					pdh_user_id=EXCLUDED.pdh_user_id,
					email=EXCLUDED.email,
					display_name=EXCLUDED.display_name,
					department=EXCLUDED.department,
					account_enabled=EXCLUDED.account_enabled,
					synced_at=NOW(),
					given_name=EXCLUDED.given_name, surname=EXCLUDED.surname, job_title=EXCLUDED.job_title,
					business_phone=EXCLUDED.business_phone, mobile_phone=EXCLUDED.mobile_phone,
					office_location=EXCLUDED.office_location, company_name=EXCLUDED.company_name`,
				h.microsoft.TenantID, directoryUser.ID, pdhUserID, email, directoryUser.DisplayName, directoryUser.Department, directoryUser.AccountEnabled,
				directoryUser.GivenName, directoryUser.Surname, directoryUser.JobTitle, directoryUser.businessPhone(),
				directoryUser.MobilePhone, directoryUser.OfficeLocation, directoryUser.CompanyName)
			if err != nil {
				return res, err
			}
			res.Matched++
			if provision && directoryUser.AccountEnabled {
				switch linked, err := h.provisionNextcloudFromMicrosoft(ctx, pdhUserID, email, directoryUser.DisplayName, actorID); {
				case err != nil:
					res.NextcloudFailed++
					componentLog("nextcloud").Warn().Err(err).Str("user", pdhUserID).Str("microsoft", email).Msg("konto beim microsoft-abgleich")
				case linked:
					res.NextcloudCreated++
				}
			}
		}
		nextURL = page.NextLink
	}
	return res, nil
}

// provisionNextcloudFromMicrosoft legt für eine zugeordnete Person ohne
// Nextcloud-Konto eines mit der Microsoft-E-Mail als Benutzer-ID an.
// linked=false: Person hatte schon ein Konto.
func (h *Handler) provisionNextcloudFromMicrosoft(ctx context.Context, pdhUserID, email, displayName, actorID string) (linked bool, err error) {
	var current, firstName, lastName string
	if err := h.db.QueryRow(ctx, `SELECT COALESCE(nextcloud_user_id, ''), first_name, last_name FROM users WHERE id=$1::uuid`,
		pdhUserID).Scan(&current, &firstName, &lastName); err != nil {
		return false, err
	}
	if strings.TrimSpace(current) != "" {
		return false, nil
	}
	ncID, ok := nextcloudIDFromMicrosoftEmail(email)
	if !ok {
		return false, fmt.Errorf("„%s“ taugt nicht als Nextcloud-Benutzer-ID", email)
	}
	if name := strings.TrimSpace(firstName + " " + lastName); name != "" {
		displayName = name
	}
	if _, _, err := h.createAndLinkNextcloudAccount(ctx, pdhUserID, ncID, displayName, email, actorID); err != nil {
		return false, err
	}
	return true, nil
}

func (h *Handler) microsoftDirectoryToken(ctx context.Context) (string, error) {
	form := url.Values{
		"client_id":     {h.microsoft.ClientID},
		"client_secret": {h.microsoft.ClientSecret},
		"grant_type":    {"client_credentials"},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	endpoint := microsoftLoginBase + "/" + url.PathEscape(h.microsoft.TenantID) + "/oauth2/v2.0/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var e struct {
			Description string `json:"error_description"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&e)
		if desc, _, _ := strings.Cut(e.Description, "\r\n"); desc != "" {
			return "", fmt.Errorf("Microsoft token endpoint returned HTTP %d: %s", response.StatusCode, desc)
		}
		return "", fmt.Errorf("Microsoft token endpoint returned HTTP %d", response.StatusCode)
	}
	var tokens microsoftTokenResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens); err != nil {
		return "", err
	}
	if tokens.AccessToken == "" {
		return "", fmt.Errorf("Microsoft returned no Graph access token")
	}
	return tokens.AccessToken, nil
}

func (h *Handler) getMicrosoftGraphJSON(ctx context.Context, accessToken, endpoint string, target interface{}) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "graph.microsoft.com" {
		return fmt.Errorf("invalid Microsoft Graph pagination URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Prefer", `outlook.timezone="UTC"`)
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Microsoft Graph returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target)
}
