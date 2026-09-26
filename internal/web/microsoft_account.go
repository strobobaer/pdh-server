package web

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const microsoftAuthority = "https://login.microsoftonline.com/common/oauth2/v2.0"

type MicrosoftOAuthConfig struct {
	ClientID          string
	ClientSecret      string
	RedirectURL       string
	TenantID          string
	TeamsSenderUserID string
	TeamsID           string
	TeamsChannelID    string
}

type AccountPageData struct {
	BaseData
	MicrosoftConfigured bool
	MicrosoftConnected  bool
	MicrosoftAccountLabel string
	MicrosoftTeamsPermission bool
	SyncShifts           bool
	SyncTasks            bool
	SyncMaintenance      bool
	SyncTicketsFaults    bool
	ImportBusyEvents     bool
	CalendarLastSync     string
	CalendarBusyCount    int
	CalendarBusyBlocks   []MicrosoftBusyBlockView
	Notice              string
}

type MicrosoftBusyBlockView struct {
	StartsAt string
	EndsAt   string
	AllDay   bool
}

type microsoftTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

type microsoftProfile struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

func (h *Handler) ConfigureMicrosoft(config MicrosoftOAuthConfig) {
	h.microsoft = config
}

func (c MicrosoftOAuthConfig) configured() bool {
	return strings.TrimSpace(c.ClientID) != "" && strings.TrimSpace(c.ClientSecret) != "" && strings.TrimSpace(c.RedirectURL) != ""
}

func (h *Handler) AccountPage(w http.ResponseWriter, r *http.Request) {
	data := AccountPageData{
		BaseData:            h.baseData(r, "account", "Mein Konto", "Konto und verbundene Dienste"),
		MicrosoftConfigured: h.microsoft.configured(),
		Notice:              r.URL.Query().Get("notice"),
	}
	var email, displayName, microsoftUserID, grantedScopes string
	err := h.db.QueryRow(r.Context(), `
		SELECT microsoft_email, display_name, microsoft_user_id, granted_scopes
		FROM microsoft_user_connections WHERE user_id=$1::uuid`, getUser(r).ID,
	).Scan(&email, &displayName, &microsoftUserID, &grantedScopes)
	if err == nil {
		data.MicrosoftConnected = true
		data.MicrosoftAccountLabel = email
		if data.MicrosoftAccountLabel == "" {
			data.MicrosoftAccountLabel = displayName
		}
		if data.MicrosoftAccountLabel == "" {
			data.MicrosoftAccountLabel = microsoftUserID
		}
		data.MicrosoftTeamsPermission = strings.Contains(grantedScopes, "Chat.ReadWrite") && strings.Contains(grantedScopes, "ChannelMessage.Send")
	}
	_ = h.db.QueryRow(r.Context(), `
		SELECT sync_shifts, sync_tasks, sync_maintenance, sync_tickets_faults, import_busy_events,
		       COALESCE(to_char(last_sync_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI'), '')
		FROM microsoft_calendar_preferences WHERE user_id=$1::uuid`, getUser(r).ID,
	).Scan(&data.SyncShifts, &data.SyncTasks, &data.SyncMaintenance, &data.SyncTicketsFaults, &data.ImportBusyEvents, &data.CalendarLastSync)
	_ = h.db.QueryRow(r.Context(), `SELECT COUNT(*) FROM microsoft_calendar_blocks WHERE user_id=$1::uuid`, getUser(r).ID).Scan(&data.CalendarBusyCount)
	blockRows, err := h.db.Query(r.Context(), `
		SELECT starts_at, ends_at, is_all_day FROM microsoft_calendar_blocks
		WHERE user_id=$1::uuid AND ends_at > NOW()
		ORDER BY starts_at LIMIT 12`, getUser(r).ID)
	if err == nil {
		defer blockRows.Close()
		for blockRows.Next() {
			var block MicrosoftBusyBlockView
			var startsAt, endsAt time.Time
			if blockRows.Scan(&startsAt, &endsAt, &block.AllDay) == nil {
				block.StartsAt = startsAt.Local().Format("02.01.2006 15:04")
				block.EndsAt = endsAt.Local().Format("02.01.2006 15:04")
				data.CalendarBusyBlocks = append(data.CalendarBusyBlocks, block)
			}
		}
	}
	h.render(w, "account", data)
}

func (h *Handler) MicrosoftConnectStart(w http.ResponseWriter, r *http.Request) {
	h.startMicrosoftConnect(w, r, "calendar")
}

func (h *Handler) MicrosoftTeamsConnectStart(w http.ResponseWriter, r *http.Request) {
	h.startMicrosoftConnect(w, r, "teams")
}

func (h *Handler) startMicrosoftConnect(w http.ResponseWriter, r *http.Request, scopeMode string) {
	if !h.microsoft.configured() {
		http.Redirect(w, r, "/account?notice=Microsoft+OAuth+nicht+konfiguriert", http.StatusSeeOther)
		return
	}
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		http.Error(w, "OAuth konnte nicht gestartet werden", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		http.Error(w, "OAuth konnte nicht gestartet werden", http.StatusInternalServerError)
		return
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	encryptedVerifier, err := h.encryptMicrosoftToken(codeVerifier)
	if err != nil {
		http.Error(w, "OAuth konnte nicht gestartet werden", http.StatusInternalServerError)
		return
	}
	challengeHash := sha256.Sum256([]byte(codeVerifier))
	userID := getUser(r).ID
	_, err = h.db.Exec(r.Context(), `DELETE FROM microsoft_oauth_states WHERE user_id=$1::uuid OR expires_at <= NOW()`, userID)
	if err != nil {
		http.Error(w, "OAuth konnte nicht gestartet werden", http.StatusInternalServerError)
		return
	}
	_, err = h.db.Exec(r.Context(), `
		INSERT INTO microsoft_oauth_states (state_hash, code_verifier, scope_mode, user_id, expires_at)
		VALUES ($1, $2, $3, $4::uuid, NOW() + INTERVAL '10 minutes')`, microsoftStateHash(state), encryptedVerifier, scopeMode, userID)
	if err != nil {
		http.Error(w, "OAuth konnte nicht gestartet werden", http.StatusInternalServerError)
		return
	}
	params := url.Values{
		"client_id":     {h.microsoft.ClientID},
		"response_type": {"code"},
		"redirect_uri":  {h.microsoft.RedirectURL},
		"response_mode": {"query"},
		"scope":         {microsoftScopes(scopeMode)},
		"state":         {state},
		"prompt":        {"select_account"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challengeHash[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, microsoftAuthority+"/authorize?"+params.Encode(), http.StatusFound)
}

func (h *Handler) MicrosoftConnectCallback(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("error") != "" {
		http.Redirect(w, r, "/account?notice=Microsoft-Anmeldung+abgebrochen", http.StatusSeeOther)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		http.Redirect(w, r, "/account?notice=Ungültige+Microsoft-Antwort", http.StatusSeeOther)
		return
	}
	var stateUserID, codeVerifier, scopeMode string
	err := h.db.QueryRow(r.Context(), `
		DELETE FROM microsoft_oauth_states
		WHERE state_hash=$1 AND user_id=$2::uuid AND expires_at > NOW()
		RETURNING user_id::text, code_verifier, scope_mode`, microsoftStateHash(state), getUser(r).ID).Scan(&stateUserID, &codeVerifier, &scopeMode)
	if err != nil || stateUserID != getUser(r).ID {
		http.Redirect(w, r, "/account?notice=Microsoft-Anmeldung+abgelaufen", http.StatusSeeOther)
		return
	}
	codeVerifier, err = h.decryptMicrosoftToken(codeVerifier)
	if err != nil {
		http.Error(w, "OAuth-Status konnte nicht geprüft werden", http.StatusInternalServerError)
		return
	}
	tokens, err := h.exchangeMicrosoftCode(r.Context(), code, codeVerifier, microsoftScopes(scopeMode))
	if err != nil {
		http.Redirect(w, r, "/account?notice=Microsoft-Tokenaustausch+fehlgeschlagen", http.StatusSeeOther)
		return
	}
	profile, err := h.fetchMicrosoftProfile(r.Context(), tokens.AccessToken)
	if err != nil {
		http.Redirect(w, r, "/account?notice=Microsoft-Profil+konnte+nicht+geladen+werden", http.StatusSeeOther)
		return
	}
	if profile.ID == "" || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		http.Redirect(w, r, "/account?notice=Microsoft-Antwort+unvollständig", http.StatusSeeOther)
		return
	}
	email := strings.TrimSpace(profile.Mail)
	if email == "" {
		email = strings.TrimSpace(profile.UserPrincipalName)
	}
	accessToken, err := h.encryptMicrosoftToken(tokens.AccessToken)
	if err != nil {
		http.Error(w, "Microsoft-Token konnte nicht geschützt gespeichert werden", http.StatusInternalServerError)
		return
	}
	refreshToken, err := h.encryptMicrosoftToken(tokens.RefreshToken)
	if err != nil {
		http.Error(w, "Microsoft-Token konnte nicht geschützt gespeichert werden", http.StatusInternalServerError)
		return
	}
	expiresIn := tokens.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)
	grantedScopes := tokens.Scope
	if grantedScopes == "" {
		grantedScopes = microsoftScopes(scopeMode)
	}
	_, err = h.db.Exec(r.Context(), `
		INSERT INTO microsoft_user_connections
			(user_id, microsoft_user_id, microsoft_email, display_name, access_token, refresh_token, granted_scopes, expires_at, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (user_id) DO UPDATE SET
			microsoft_user_id=EXCLUDED.microsoft_user_id,
			microsoft_email=EXCLUDED.microsoft_email,
			display_name=EXCLUDED.display_name,
			access_token=EXCLUDED.access_token,
			refresh_token=EXCLUDED.refresh_token,
			granted_scopes=EXCLUDED.granted_scopes,
			expires_at=EXCLUDED.expires_at,
			updated_at=NOW()`, getUser(r).ID, profile.ID, email, profile.DisplayName, accessToken, refreshToken, grantedScopes, expiresAt)
	if err != nil {
		http.Redirect(w, r, "/account?notice=Dieses+Microsoft-Konto+konnte+nicht+verknüpft+werden", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/account?notice=Microsoft-Konto+verbunden", http.StatusSeeOther)
}

func (h *Handler) MicrosoftDisconnect(w http.ResponseWriter, r *http.Request) {
	_, err := h.db.Exec(r.Context(), `DELETE FROM microsoft_user_connections WHERE user_id=$1::uuid`, getUser(r).ID)
	if err != nil {
		http.Error(w, "Microsoft-Konto konnte nicht getrennt werden", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account?notice=Microsoft-Konto+getrennt", http.StatusSeeOther)
}

func (h *Handler) exchangeMicrosoftCode(ctx context.Context, code, codeVerifier, scopes string) (microsoftTokenResponse, error) {
	var tokens microsoftTokenResponse
	form := url.Values{
		"client_id":     {h.microsoft.ClientID},
		"client_secret": {h.microsoft.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {codeVerifier},
		"redirect_uri":  {h.microsoft.RedirectURL},
		"scope":         {scopes},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, microsoftAuthority+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokens, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return tokens, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return tokens, fmt.Errorf("Microsoft token endpoint returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&tokens); err != nil {
		return tokens, err
	}
	return tokens, nil
}

func (h *Handler) fetchMicrosoftProfile(ctx context.Context, accessToken string) (microsoftProfile, error) {
	var profile microsoftProfile
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me?$select=id,displayName,mail,userPrincipalName", nil)
	if err != nil {
		return profile, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return profile, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return profile, fmt.Errorf("Microsoft Graph returned HTTP %d", response.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&profile)
	return profile, err
}

func (h *Handler) encryptMicrosoftToken(plain string) (string, error) {
	key := sha256.Sum256([]byte("pdh/microsoft-token/v1/" + h.jwtSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (h *Handler) decryptMicrosoftToken(encoded string) (string, error) {
	sealed, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte("pdh/microsoft-token/v1/" + h.jwtSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", fmt.Errorf("Microsoft token ciphertext is truncated")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func microsoftStateHash(state string) string {
	hash := sha256.Sum256([]byte(state))
	return hex.EncodeToString(hash[:])
}

func microsoftScopes(mode string) string {
	scopes := "openid profile email offline_access User.Read Calendars.ReadWrite"
	if mode == "teams" {
		scopes += " Chat.ReadWrite ChannelMessage.Send"
	}
	return scopes
}