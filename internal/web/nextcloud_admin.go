package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"pdh/internal/integrations/nextcloud"
)

// Dienstkonto des PDH in Nextcloud (Standard „pdhNCadmin“, Gruppe admin):
// das PDH nutzt es für alles, was es in Nextcloud pflegt (Konten anlegen,
// Ablage, Deck, Theming). Das Passwort verwaltet das PDH (Server-Einstellungen,
// verschlüsselt) und hat Vorrang: Der Abgleich setzt es in Nextcloud über den
// Update-Agenten (occ) durch – auch wenn es dort jemand geändert hat.

const (
	ncAdminDefaultUser = "pdhNCadmin"
	keyNCAdminManaged  = "nextcloud.service_account"         // "1" = vom PDH verwaltet
	keyNCAdminChecked  = "nextcloud.service_account_checked" // letzte Prüfung (Text)
)

var ncAdminUserRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

type ncAdminView struct {
	Managed     bool
	User        string
	Checked     string
	AgentReady  bool
	Configured  bool // Adresse eingetragen
	DefaultUser string
}

func (h *Handler) ncAdminViewFor(ctx context.Context) ncAdminView {
	return ncAdminView{
		Managed:     h.getUpdateSetting(ctx, keyNCAdminManaged, "") == "1",
		User:        strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_USERNAME")),
		Checked:     h.getUpdateSetting(ctx, keyNCAdminChecked, ""),
		AgentReady:  h.driveAgentConfigured(),
		Configured:  strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_BASEURL")) != "",
		DefaultUser: ncAdminDefaultUser,
	}
}

// applyNCServiceAccount: Konto in Nextcloud anlegen bzw. Passwort setzen
// (Update-Agent, occ). Liefert „created“ oder „updated“.
func (h *Handler) applyNCServiceAccount(ctx context.Context, user, password string) (string, error) {
	var res struct {
		Status string `json:"status"`
	}
	err := h.agentJSON(ctx, http.MethodPost, "/v1/nextcloud/service-account", map[string]any{
		"dir": h.ncDir(ctx), "user": user, "password": password,
	}, &res)
	return res.Status, err
}

// NextcloudAdminWeb: POST /admin/server-config/nextcloud/admin
// action=sync (einrichten/abgleichen) | rotate (neues Passwort) | release (nicht mehr verwalten)
func (h *Handler) NextcloudAdminWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canServerConfig(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	back := func(msg string, err error) {
		key, text := "msg", msg
		if err != nil {
			key, text = "err", err.Error()
		}
		http.Redirect(w, r, "/admin/server-config?tab=nextcloud&"+key+"="+url.QueryEscape(text), http.StatusSeeOther)
	}
	action := r.FormValue("action")
	if action == "release" {
		err := h.setUpdateSetting(ctx, keyNCAdminManaged, "0")
		back("Das PDH verwaltet das Nextcloud-Konto nicht mehr – das Passwort bleibt, wie es ist", err)
		return
	}
	user := strings.TrimSpace(r.FormValue("user"))
	if user == "" {
		user = ncAdminDefaultUser
	}
	if !ncAdminUserRe.MatchString(user) {
		back("", errors.New("Benutzername: nur Buchstaben, Ziffern und _ . @ -"))
		return
	}
	if strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_BASEURL")) == "" {
		back("", errors.New("Zuerst oben die Nextcloud-Adresse eintragen und speichern"))
		return
	}
	pw := os.Getenv("PDH_NEXTCLOUD_PASSWORD")
	newPW := action == "rotate" || len(pw) < 12 || user != strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_USERNAME"))
	if newPW {
		pw = nextcloud.RandomPassword(32)
	}
	// 1. in Nextcloud setzen (PDH hat Vorrang), 2. Anmeldung prüfen, 3. im PDH speichern
	actx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	status, err := h.applyNCServiceAccount(actx, user, pw)
	if err != nil {
		componentLog("nextcloud").Warn().Err(err).Str("konto", user).Msg("dienstkonto einrichten")
		back("", fmt.Errorf("Nextcloud-Konto nicht eingerichtet: %w", err))
		return
	}
	isAdmin, cerr := nextcloud.NewUserClient(os.Getenv("PDH_NEXTCLOUD_BASEURL"), user, pw).CheckAdmin(actx)
	if newPW {
		if _, err := h.applyEnvUpdates(ctx, map[string]string{"PDH_NEXTCLOUD_USERNAME": user, "PDH_NEXTCLOUD_PASSWORD": pw}, getUser(r).ID); err != nil {
			back("", fmt.Errorf("In Nextcloud gesetzt, aber im PDH nicht gespeichert: %w – bitte erneut „Abgleichen“", err))
			return
		}
	}
	_ = h.setUpdateSetting(ctx, keyNCAdminManaged, "1")
	note := time.Now().Local().Format("02.01.2006 15:04") + " – angemeldet"
	switch {
	case cerr != nil:
		note = time.Now().Local().Format("02.01.2006 15:04") + " – Anmeldung geprüft: " + cerr.Error()
	case !isAdmin:
		note = time.Now().Local().Format("02.01.2006 15:04") + " – angemeldet, aber nicht in der Gruppe admin"
	}
	_ = h.setUpdateSetting(ctx, keyNCAdminChecked, note)
	componentLog("nextcloud").Info().Str("konto", user).Str("status", status).Bool("neues_passwort", newPW).Str("von", getUser(r).ID).Msg("dienstkonto")
	msg := "Nextcloud-Konto „" + user + "“ "
	if status == "created" {
		msg += "angelegt"
	} else {
		msg += "abgeglichen"
	}
	if newPW {
		msg += " – neues Passwort gesetzt und gespeichert. Ablage und Deck übernehmen es nach einem Neustart"
	}
	if cerr != nil {
		back("", fmt.Errorf("%s, aber die Anmeldung schlägt fehl: %v (Zwei-Faktor-Pflicht für Admins? Dann App-Passwort nötig)", msg, cerr))
		return
	}
	back(msg, nil)
}

// StartNextcloudAdminSync: alle 6 Stunden prüfen, ob das Dienstkonto noch
// hineinkommt; sonst das PDH-Passwort in Nextcloud wieder setzen (Vorrang PDH).
func (h *Handler) StartNextcloudAdminSync(ctx context.Context) {
	if h.db == nil {
		return
	}
	go func() {
		timer := time.NewTimer(4 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				runCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				h.syncNCServiceAccount(runCtx)
				cancel()
				timer.Reset(6 * time.Hour)
			}
		}
	}()
}

func (h *Handler) syncNCServiceAccount(ctx context.Context) {
	if h.getUpdateSetting(ctx, keyNCAdminManaged, "") != "1" || !h.driveAgentConfigured() {
		return
	}
	c := nextcloud.UserClientFromEnv()
	if !c.Enabled() {
		return
	}
	stamp := time.Now().Local().Format("02.01.2006 15:04")
	isAdmin, err := c.CheckAdmin(ctx)
	if err == nil && isAdmin {
		_ = h.setUpdateSetting(ctx, keyNCAdminChecked, stamp+" – angemeldet")
		return
	}
	if err != nil && !errors.Is(err, nextcloud.ErrLoginFailed) {
		// Nextcloud nicht erreichbar o. Ä. – nichts erzwingen
		_ = h.setUpdateSetting(ctx, keyNCAdminChecked, stamp+" – nicht geprüft: "+err.Error())
		return
	}
	user := strings.TrimSpace(os.Getenv("PDH_NEXTCLOUD_USERNAME"))
	if _, aerr := h.applyNCServiceAccount(ctx, user, os.Getenv("PDH_NEXTCLOUD_PASSWORD")); aerr != nil {
		componentLog("nextcloud").Error().Err(aerr).Str("konto", user).Msg("dienstkonto: passwort nicht durchgesetzt")
		_ = h.setUpdateSetting(ctx, keyNCAdminChecked, stamp+" – Abgleich fehlgeschlagen: "+aerr.Error())
		h.notifyAdmins(ctx, "⚠️ **Nextcloud-Dienstkonto „"+user+"“**: Anmeldung schlägt fehl, und das PDH konnte sein Passwort nicht wieder setzen ("+aerr.Error()+"). Server-Einstellungen → Nextcloud prüfen.")
		return
	}
	why := "Passwort in Nextcloud war geändert"
	if err == nil && !isAdmin {
		why = "Konto war nicht mehr in der Gruppe admin"
	}
	_ = h.setUpdateSetting(ctx, keyNCAdminChecked, stamp+" – wiederhergestellt ("+why+")")
	componentLog("nextcloud").Warn().Str("konto", user).Str("grund", why).Msg("dienstkonto wiederhergestellt")
	h.notifyAdmins(ctx, "🔐 **Nextcloud-Dienstkonto „"+user+"“** wiederhergestellt: "+why+" – das PDH hat sein Passwort und die Admin-Rechte wieder gesetzt.")
}
