package web

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Portal-/Shop-Links eines Partners inkl. verschluesselter Zugangsdaten.
// Einbettung ist je Link schaltbar (viele Portale verbieten iframes per
// X-Frame-Options/CSP - dann bleibt nur "in neuem Tab oeffnen").

var partnerLinkKinds = []struct{ Key, Label, Icon string }{
	{"support_portal", "Support-Portal", "ti-lifebuoy"},
	{"shop", "Onlineshop", "ti-shopping-cart"},
	{"ticket_system", "Ticketsystem", "ti-ticket"},
	{"documentation", "Dokumentation", "ti-book"},
	{"download", "Downloads / Software", "ti-download"},
	{"other", "Sonstiges", "ti-link"},
}

func partnerLinkKind(key string) (label, icon string, ok bool) {
	for _, k := range partnerLinkKinds {
		if k.Key == key {
			return k.Label, k.Icon, true
		}
	}
	return "", "", false
}

func (h *Handler) canPartnerCredentials(r *http.Request) bool {
	u := getUser(r)
	return h.rbac.HasPermissionForUser(u.ID, string(u.Role), "directory.credentials")
}

// credentialKey: eigener Schluessel ueber PDH_CREDENTIALS_KEY, sonst aus
// dem JWT-Secret abgeleitet. Achtung: Wechsel des Schluessels macht
// gespeicherte Passwoerter unlesbar (sie muessen neu erfasst werden).
func (h *Handler) credentialKey() []byte {
	secret := h.mailCfg.CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-partner-credentials:" + secret))
	return sum[:]
}

func encryptSecret(key []byte, plain string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func decryptSecret(key []byte, enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, "v1:"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ungültiger Schlüsseltext")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("Passwort kann nicht entschlüsselt werden (Schlüssel geändert?)")
	}
	return string(plain), nil
}

// PartnerLinkView ist ein Portal-/Shop-Link auf der Partner-Detailseite.
type PartnerLinkView struct {
	ID, Kind, KindLabel, Icon, Label, URL string
	Embed                                 bool
	Username, AccountNo, Notes            string
	HasPassword                           bool
	PasswordUpdated                       string
}

func (h *Handler) partnerLinks(ctx context.Context, partnerID string) []PartnerLinkView {
	rows, err := h.db.Query(ctx, `
		SELECT id::text, kind, label, url, embed_enabled, username, account_no, notes, password_enc <> '',
		       COALESCE(to_char(password_updated_at, 'DD.MM.YYYY'), '')
		FROM business_partner_links WHERE partner_id = $1
		ORDER BY CASE kind WHEN 'support_portal' THEN 0 WHEN 'shop' THEN 1 ELSE 2 END, label`, partnerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []PartnerLinkView
	for rows.Next() {
		var l PartnerLinkView
		if rows.Scan(&l.ID, &l.Kind, &l.Label, &l.URL, &l.Embed, &l.Username, &l.AccountNo, &l.Notes,
			&l.HasPassword, &l.PasswordUpdated) != nil {
			continue
		}
		l.KindLabel, l.Icon, _ = partnerLinkKind(l.Kind)
		list = append(list, l)
	}
	return list
}

func validPortalURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("Bitte eine gültige http(s)-Adresse angeben")
	}
	return u.String(), nil
}

// PartnerLinkSaveWeb legt einen Portal-Link an oder aendert ihn. Die
// Zugangsdaten (Benutzer/Passwort) werden nur mit directory.credentials
// geschrieben; ein leeres Passwortfeld laesst das bestehende unveraendert.
func (h *Handler) PartnerLinkSaveWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	kind := v("kind")
	if _, _, ok := partnerLinkKind(kind); !ok {
		kind = "other"
	}
	link, err := validPortalURL(v("url"))
	if err != nil {
		partnerRedirect(w, r, id, "portals", "", err)
		return
	}
	label := v("label")
	if label == "" {
		label, _, _ = partnerLinkKind(kind)
	}
	ctx := r.Context()
	embed := r.FormValue("embed_enabled") == "on"
	linkID := v("link_id")
	if linkID == "" {
		err = h.db.QueryRow(ctx, `
			INSERT INTO business_partner_links (partner_id, kind, label, url, embed_enabled, account_no, notes, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
			id, kind, label, link, embed, v("account_no"), v("notes"), nullID(getUser(r).ID)).Scan(&linkID)
	} else {
		_, err = h.db.Exec(ctx, `
			UPDATE business_partner_links SET kind=$1, label=$2, url=$3, embed_enabled=$4, account_no=$5, notes=$6, updated_at=NOW()
			WHERE id=$7 AND partner_id=$8`, kind, label, link, embed, v("account_no"), v("notes"), linkID, id)
	}
	if err == nil && h.canPartnerCredentials(r) {
		_, err = h.db.Exec(ctx, `UPDATE business_partner_links SET username=$1 WHERE id=$2 AND partner_id=$3`, v("username"), linkID, id)
		if pw := r.FormValue("password"); err == nil && (pw != "" || r.FormValue("clear_password") == "on") {
			enc := ""
			if pw != "" {
				enc, err = encryptSecret(h.credentialKey(), pw)
			}
			if err == nil {
				_, err = h.db.Exec(ctx, `
					UPDATE business_partner_links SET password_enc=$1, password_updated_at=CASE WHEN $1 = '' THEN NULL ELSE NOW() END
					WHERE id=$2 AND partner_id=$3`, enc, linkID, id)
			}
			if err == nil {
				h.logPartnerCredentialEvent(ctx, r, id, linkID, "credentials_update", "Zugangsdaten geändert")
			}
		}
	}
	partnerRedirect(w, r, id, "portals", "Link gespeichert", err)
}

func (h *Handler) PartnerLinkDeleteWeb(w http.ResponseWriter, r *http.Request) {
	id, lid := chi.URLParam(r, "id"), chi.URLParam(r, "linkId")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(r.Context(), `DELETE FROM business_partner_links WHERE id=$1 AND partner_id=$2`, lid, id)
	partnerRedirect(w, r, id, "portals", "Link entfernt", err)
}

// PartnerLinkEmbedWeb schaltet die Einbettung eines Links um.
func (h *Handler) PartnerLinkEmbedWeb(w http.ResponseWriter, r *http.Request) {
	id, lid := chi.URLParam(r, "id"), chi.URLParam(r, "linkId")
	if !h.canEditPartners(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, err := h.db.Exec(r.Context(), `
		UPDATE business_partner_links SET embed_enabled = NOT embed_enabled, updated_at = NOW()
		WHERE id=$1 AND partner_id=$2`, lid, id)
	partnerRedirect(w, r, id, "portals", "Einbettung umgeschaltet", err)
}

// PartnerLinkPasswordWeb liefert das entschluesselte Passwort (nur mit
// directory.credentials, jede Anzeige wird in der Historie protokolliert).
func (h *Handler) PartnerLinkPasswordWeb(w http.ResponseWriter, r *http.Request) {
	id, lid := chi.URLParam(r, "id"), chi.URLParam(r, "linkId")
	w.Header().Set("Cache-Control", "no-store")
	if !h.canPartnerCredentials(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var enc string
	if err := h.db.QueryRow(r.Context(), `SELECT password_enc FROM business_partner_links WHERE id=$1 AND partner_id=$2`, lid, id).Scan(&enc); err != nil {
		http.Error(w, "nicht gefunden", http.StatusNotFound)
		return
	}
	plain, err := decryptSecret(h.credentialKey(), enc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	h.logPartnerCredentialEvent(r.Context(), r, id, lid, "credentials_view", "Passwort angezeigt")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, plain)
}

func (h *Handler) logPartnerCredentialEvent(ctx context.Context, r *http.Request, partnerID, linkID, action, msg string) {
	_, _ = h.db.Exec(ctx, `
		INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
		SELECT 'business_partner', $1::uuid, $2::text, 'portal_link', label, $3::uuid, $4::text FROM business_partner_links WHERE id = $5::uuid`,
		partnerID, action, nullID(getUser(r).ID), msg, linkID)
}
