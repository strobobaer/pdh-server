package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// PDH-App: Seite „App-Geräte“ (Geräteliste, Sperren, offene Anfragen, Einstellungen)
// und „App installieren“ (QR-Code für das Telefon).
//
//	GET  /app/devices              Liste, offene Anfragen, Einstellungen (app.admin irgendwo)
//	POST /app/devices/{id}/revoke  Gerät sperren
//	POST /app/settings             App-Einstellungen speichern (nur app.admin überall)
//	GET  /app/install              QR-Code und Store-Knöpfe (app.use)
//
// Sichtbarkeit: ein Gerät zeigt die Liste nur, wenn der Handelnde app.admin in der
// Abteilung des Benutzers hat (appAdminIn). Sperren zusätzlich nur, wenn seine Rolle
// die des Benutzers übertrifft – dieselbe Prüfung wie beim Freigeben.

// appDeviceRow: eine Zeile der Geräteliste (auch am Benutzer).
type appDeviceRow struct {
	ID, UserID, UserName, Label, Platform, Version string
	Created, LastSeen, Revoked                     string // deutsch formatiert; LastSeen „nie“, Revoked leer = aktiv
	CanRevoke                                      bool
}

type appPendingRow struct {
	ID, Name, DeptName, Label, Platform, Version, Created string
}

// appSettingsForm: Werte des Einstellungsformulars (Textform, wie eingegeben).
type appSettingsForm struct {
	Addresses     string
	AllowPrevious bool
	LatestIOS     string
	LatestAndroid string
	StoreIOS      string
	StoreAndroid  string
}

type appDevicesPageData struct {
	BaseData
	Devices     []appDeviceRow
	Requests    []appPendingRow
	CanSettings bool
	Form        appSettingsForm
	Saved       bool
	Revoked     bool
	Approved    bool
	Rejected    bool
	Error       string
}

const appTimeLayout = "02.01.2006 15:04"

// appRevokeAllowed: darf der Handelnde Geräte dieses Benutzers sperren?
// Abteilung des Benutzers (app.admin) und Rang der Rolle.
func (h *Handler) appRevokeAllowed(r *http.Request, userRole, userDept string) bool {
	return h.rbac != nil && h.rbac.Outranks(string(getUser(r).Role), userRole) && h.appAdminIn(r, userDept)
}

// loadAppDevices liest die Geräte (alle oder die eines Benutzers) und behält nur die,
// deren Benutzer der Handelnde sehen darf. Aktive zuerst, neueste zuerst.
func (h *Handler) loadAppDevices(r *http.Request, onlyUser string) ([]appDeviceRow, error) {
	rows, err := h.db.Query(r.Context(), `SELECT d.id::text, u.id::text, u.first_name, u.last_name, u.username, u.role,
		COALESCE(u.department_id::text, ''), d.label, d.platform, d.app_version, d.created_at, d.last_seen_at, d.revoked_at
		FROM app_devices d JOIN users u ON u.id = d.user_id
		WHERE ($1 = '' OR d.user_id = NULLIF($1, '')::uuid)
		ORDER BY (d.revoked_at IS NOT NULL), d.created_at DESC, d.id`, onlyUser)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []appDeviceRow
	for rows.Next() {
		var d appDeviceRow
		var first, last, username, role, dept string
		var created time.Time
		var seen, revoked *time.Time
		if err := rows.Scan(&d.ID, &d.UserID, &first, &last, &username, &role, &dept, &d.Label, &d.Platform, &d.Version, &created, &seen, &revoked); err != nil {
			return nil, err
		}
		if !h.appAdminIn(r, dept) {
			continue
		}
		d.UserName = displayUserName(first, last, username)
		d.Platform = appPlatformName(d.Platform)
		d.Created = created.Local().Format(appTimeLayout)
		d.LastSeen = "nie"
		if seen != nil {
			d.LastSeen = seen.Local().Format(appTimeLayout)
		}
		if revoked != nil {
			d.Revoked = revoked.Local().Format(appTimeLayout)
		} else {
			d.CanRevoke = h.rbac.Outranks(string(getUser(r).Role), role)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (h *Handler) loadAppPending(r *http.Request) ([]appPendingRow, error) {
	rows, err := h.db.Query(r.Context(), `SELECT q.id::text, q.name, COALESCE(q.department_id::text, ''), COALESCE(dp.name, ''),
		q.device_label, q.platform, q.app_version, q.created_at
		FROM app_requests q LEFT JOIN departments dp ON dp.id = q.department_id
		WHERE q.status = 'pending' ORDER BY q.created_at, q.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []appPendingRow
	for rows.Next() {
		var p appPendingRow
		var dept string
		var created time.Time
		if err := rows.Scan(&p.ID, &p.Name, &dept, &p.DeptName, &p.Label, &p.Platform, &p.Version, &created); err != nil {
			return nil, err
		}
		if !h.appAdminIn(r, dept) {
			continue
		}
		p.Platform = appPlatformName(p.Platform)
		p.Created = created.Local().Format(appTimeLayout)
		out = append(out, p)
	}
	return out, rows.Err()
}

// loadAppSettingsForm liest die gespeicherten Einstellungen.
func (h *Handler) loadAppSettingsForm(ctx context.Context) appSettingsForm {
	return appSettingsForm{
		Addresses:     h.appSetting(ctx, keyAppAddresses, ""),
		AllowPrevious: h.appSetting(ctx, keyAppAllowPrevious, "1") == "1",
		LatestIOS:     h.appSetting(ctx, keyAppLatestPrefix+"ios", ""),
		LatestAndroid: h.appSetting(ctx, keyAppLatestPrefix+"android", ""),
		StoreIOS:      h.appSetting(ctx, keyAppStorePrefix+"ios", ""),
		StoreAndroid:  h.appSetting(ctx, keyAppStorePrefix+"android", ""),
	}
}

func (h *Handler) renderAppDevices(w http.ResponseWriter, r *http.Request, status int, form *appSettingsForm, msg string) {
	d := &appDevicesPageData{
		BaseData:    h.baseData(r, "app_devices", "App-Geräte", ""),
		CanSettings: h.appAdminIn(r, ""),
		Saved:       r.URL.Query().Get("saved") == "1",
		Revoked:     r.URL.Query().Get("revoked") == "1",
		Approved:    r.URL.Query().Get("approved") == "1",
		Rejected:    r.URL.Query().Get("rejected") == "1",
		Error:       msg,
	}
	var err error
	if d.Devices, err = h.loadAppDevices(r, ""); err == nil {
		d.Requests, err = h.loadAppPending(r)
	}
	if err != nil {
		componentLog("app").Error().Err(err).Msg("app: geräteliste")
		http.Error(w, "Geräteliste konnte nicht geladen werden", http.StatusInternalServerError)
		return
	}
	if form != nil {
		d.Form = *form
	} else if d.CanSettings {
		d.Form = h.loadAppSettingsForm(r.Context())
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	h.render(w, "app_devices", d)
}

// AppDevicesPage: GET /app/devices.
func (h *Handler) AppDevicesPage(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permAppAdmin) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	h.renderAppDevices(w, r, http.StatusOK, nil, "")
}

// AppDeviceRevokeWeb: POST /app/devices/{id}/revoke.
func (h *Handler) AppDeviceRevokeWeb(w http.ResponseWriter, r *http.Request) {
	actor := getUser(r)
	if !h.hasPerm(r, permAppAdmin) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	id := chi.URLParam(r, "id")
	if !appUUIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	serverErr := func(err error) {
		componentLog("app").Error().Err(err).Str("device", id).Msg("app: gerät sperren")
		http.Error(w, "Sperren derzeit nicht möglich", http.StatusInternalServerError)
	}
	var userID, role, dept, label, platform string
	err := h.db.QueryRow(ctx, `SELECT u.id::text, u.role, COALESCE(u.department_id::text, ''), d.label, d.platform
		FROM app_devices d JOIN users u ON u.id = d.user_id WHERE d.id = $1::uuid`, id).Scan(&userID, &role, &dept, &label, &platform)
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	if !h.appRevokeAllowed(r, role, dept) {
		http.Error(w, "Keine Berechtigung für dieses Gerät", http.StatusForbidden)
		return
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		serverErr(err)
		return
	}
	defer tx.Rollback(ctx)
	// Nur das erste Sperren zählt: gleichzeitige Aufrufe finden danach keine Zeile mehr.
	var tag string
	err = tx.QueryRow(ctx, `UPDATE app_devices SET revoked_at = NOW(), revoked_by = $2::uuid
		WHERE id = $1::uuid AND revoked_at IS NULL RETURNING id::text`, id, actor.ID).Scan(&tag)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Das Gerät ist bereits gesperrt", http.StatusConflict)
		return
	}
	if err != nil {
		serverErr(err)
		return
	}
	name := strings.TrimSpace(label)
	if name == "" {
		name = appPlatformName(platform)
	}
	msg := fmt.Sprintf("App-Gerät gesperrt: %s", name)
	if _, err := tx.Exec(ctx, `INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, message, created_by)
		VALUES ('user', $1::uuid, 'app_device_revoked', 'App-Gerät', $2, $3, $4::uuid)`, userID, name, msg, actor.ID); err != nil {
		serverErr(err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverErr(err)
		return
	}
	appDeviceCache.forget(id) // wirkt sofort, ohne die 30 Sekunden des Zwischenspeichers
	componentLog("app").Info().Str("device", id).Str("user", userID).Str("von", actor.ID).Msg("app: gerät gesperrt")
	back := "/app/devices?revoked=1"
	if r.FormValue("back") == "user" {
		back = "/users/" + userID + "?tab=overview&msg=" + url.QueryEscape(tr(h.requestLang(r), "Das App-Gerät wurde gesperrt."))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// appCleanAddresses prüft die Adressen (eine je Zeile, nur http/https mit Host) und gibt
// sie ohne Leerzeilen, abschließenden Schrägstrich und Dubletten zurück.
func appCleanAddresses(raw string) (string, bool) {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" ||
			u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(line, "#") {
			return "", false
		}
		line = strings.TrimRight(line, "/")
		if seen[strings.ToLower(line)] {
			continue
		}
		seen[strings.ToLower(line)] = true
		out = append(out, line)
	}
	return strings.Join(out, "\n"), true
}

// AppSettingsSaveWeb: POST /app/settings (nur app.admin überall).
func (h *Handler) AppSettingsSaveWeb(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permAppAdmin) || !h.appAdminIn(r, "") {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, appFormMaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Eingabe nicht lesbar", http.StatusBadRequest)
		return
	}
	lang := h.requestLang(r)
	f := appSettingsForm{
		Addresses:     r.FormValue("addresses"),
		AllowPrevious: r.FormValue("allow_previous") == "1",
		LatestIOS:     strings.TrimSpace(r.FormValue("latest_ios")),
		LatestAndroid: strings.TrimSpace(r.FormValue("latest_android")),
		StoreIOS:      strings.TrimSpace(r.FormValue("store_ios")),
		StoreAndroid:  strings.TrimSpace(r.FormValue("store_android")),
	}
	bad := func(msg string) { h.renderAppDevices(w, r, http.StatusUnprocessableEntity, &f, tr(lang, msg)) }
	addrs, ok := appCleanAddresses(f.Addresses)
	if !ok {
		bad("Adressen: eine je Zeile, nur mit http:// oder https:// und Rechnername.")
		return
	}
	for _, v := range []string{f.LatestIOS, f.LatestAndroid} {
		if _, ok := appVersionParts(v); v != "" && !ok {
			bad("Versionen nur als Zahlen mit Punkten, z. B. 1.2.0.")
			return
		}
	}
	for _, s := range []string{f.StoreIOS, f.StoreAndroid} {
		if s != "" && appStoreLink(s) == "" {
			bad("Store-Adressen nur mit http:// oder https://.")
			return
		}
	}
	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		componentLog("app").Error().Err(err).Msg("app: einstellungen speichern")
		http.Error(w, "Speichern derzeit nicht möglich", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	put := func(key, value string) error {
		_, err := tx.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, value)
		return err
	}
	allow := "0"
	if f.AllowPrevious {
		allow = "1"
	}
	err = put(keyAppAddresses, addrs)
	if err == nil {
		err = put(keyAppAllowPrevious, allow)
	}
	for _, p := range []struct{ platform, latest, store string }{{"ios", f.LatestIOS, f.StoreIOS}, {"android", f.LatestAndroid, f.StoreAndroid}} {
		if err != nil {
			break
		}
		// Ändert sich die neueste Version, wandert der alte Wert zur vorigen Version.
		var old string
		qerr := tx.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1 FOR UPDATE`, keyAppLatestPrefix+p.platform).Scan(&old)
		if qerr != nil && !errors.Is(qerr, pgx.ErrNoRows) {
			err = qerr
			break
		}
		old = strings.TrimSpace(old)
		switch {
		case p.latest == "":
			err = put(keyAppPrevPrefix+p.platform, "") // „neueste Version“ geleert: auch die vorige entfällt
		case old != "":
			a, b := appVersionLess(old, p.latest)
			c, _ := appVersionLess(p.latest, old)
			if !b || a || c { // wirklich eine andere Version (1.2 und 1.2.0 sind gleich)
				err = put(keyAppPrevPrefix+p.platform, old)
			}
		}
		if err == nil {
			err = put(keyAppLatestPrefix+p.platform, p.latest)
		}
		if err == nil {
			err = put(keyAppStorePrefix+p.platform, p.store)
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		componentLog("app").Error().Err(err).Msg("app: einstellungen speichern")
		http.Error(w, "Speichern derzeit nicht möglich", http.StatusInternalServerError)
		return
	}
	componentLog("app").Info().Str("von", getUser(r).ID).Msg("app: einstellungen gespeichert")
	http.Redirect(w, r, "/app/devices?saved=1", http.StatusSeeOther)
}

type appInstallPageData struct {
	BaseData
	QR       template.HTML
	SetupURL string
	IOS      string
	Android  string
}

// AppInstallPage: GET /app/install – QR-Code zur Einrichtungsseite, Store-Knöpfe.
func (h *Handler) AppInstallPage(w http.ResponseWriter, r *http.Request) {
	if !h.hasPerm(r, permAppUse) {
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
		return
	}
	d := &appInstallPageData{
		BaseData: h.baseData(r, "app_install", "App installieren", ""),
		SetupURL: h.publicBaseURL(r) + "/app/setup",
		IOS:      appStoreLink(h.appSetting(r.Context(), keyAppStorePrefix+"ios", "")),
		Android:  appStoreLink(h.appSetting(r.Context(), keyAppStorePrefix+"android", "")),
	}
	qr, err := qrSVG(d.SetupURL)
	if err != nil {
		componentLog("app").Error().Err(err).Msg("app: qr-code")
		http.Error(w, "QR-Code nicht verfügbar", http.StatusInternalServerError)
		return
	}
	d.QR = qr
	w.Header().Set("Cache-Control", "no-store")
	h.render(w, "app_install", d)
}

// loadUserAppDevices füllt den Abschnitt „App-Geräte“ der Benutzerseite: nur wer app.admin
// in der Abteilung des Benutzers hat, sieht ihn (Sperren-Knopf je Gerät mit Rangprüfung).
func (h *Handler) loadUserAppDevices(r *http.Request, d *UserDetailData, userID string) {
	if h.db == nil || h.rbac == nil || !h.hasPerm(r, permAppAdmin) {
		return
	}
	var dept string
	if err := h.db.QueryRow(r.Context(), `SELECT COALESCE(department_id::text, '') FROM users WHERE id = $1::uuid`, userID).Scan(&dept); err != nil || !h.appAdminIn(r, dept) {
		return
	}
	devs, err := h.loadAppDevices(r, userID)
	if err != nil {
		componentLog("app").Error().Err(err).Str("user", userID).Msg("app: geräte des benutzers")
		return
	}
	d.ShowAppDevices, d.AppDevices = true, devs
}
