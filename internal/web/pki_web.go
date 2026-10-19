package web

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Zertifikate für Nextcloud (pki.go): Core-Einstellungen → Zertifikate
// (/core/settings/pki, Recht pki.manage) und Mein Konto → Konto & Dienste
// (eigenes Zertifikat für die Nextcloud-App, wenn Selbstbedienung an ist).

const pkiSelfServiceSetting = "pki.self_service" // "1" = Mitarbeitende stellen sich ihr Zertifikat selbst aus
const pkiSelfMax = 5                              // aktive eigene Zertifikate je Person

type pkiCertRow struct {
	ID, Kind, KindLabel, CommonName, Label, UserID, UserName, Hostnames, Serial string
	NotAfter, Created, RevokedAt, Password                                  string
	Revoked, Expired                                                        bool
}

func (c pkiCertRow) Status() (label, class string) {
	switch {
	case c.Revoked:
		return "gesperrt", "b-red"
	case c.Expired:
		return "abgelaufen", "b-gray"
	}
	return "gültig", "b-green"
}
func (c pkiCertRow) StatusLabel() string { l, _ := c.Status(); return l }
func (c pkiCertRow) StatusClass() string { _, cl := c.Status(); return cl }
func (c pkiCertRow) Active() bool        { return !c.Revoked && !c.Expired }

type PKIPageData struct {
	BaseData
	Message, Err             string
	CAName, CAValid, CAPrint string
	CAError                  string
	Certs                    []pkiCertRow
	Users                    []UserOption
	SelfService              bool
	NewID                    string
	PublicBase               string // für die nginx-Anleitung
}

func (h *Handler) canPKI(r *http.Request) bool {
	return h.hasPerm(r, "pki.manage") || h.canManageRoles(r)
}

func (h *Handler) pkiSelfService(ctx context.Context) bool {
	return h.getUpdateSetting(ctx, pkiSelfServiceSetting, "1") == "1"
}

// pkiList: Zertifikate (userID leer = alle).
func (h *Handler) pkiList(ctx context.Context, userID string) []pkiCertRow {
	q := `SELECT c.id::text, c.kind, c.common_name, c.label, COALESCE(c.user_id::text,''), COALESCE(u.first_name || ' ' || u.last_name, ''),
			c.hostnames, c.serial, c.not_after, c.created_at, c.revoked_at
		FROM pki_certificates c LEFT JOIN users u ON u.id = c.user_id`
	args := []any{}
	if userID != "" {
		q += ` WHERE c.user_id = $1::uuid`
		args = append(args, userID)
	}
	q += ` ORDER BY (c.revoked_at IS NULL) DESC, c.created_at DESC LIMIT 500`
	rows, err := h.db.Query(ctx, q, args...)
	if err != nil {
		componentLog("zertifikate").Warn().Err(err).Msg("liste")
		return nil
	}
	defer rows.Close()
	var out []pkiCertRow
	now := time.Now()
	for rows.Next() {
		var c pkiCertRow
		var notAfter, created time.Time
		var revoked *time.Time
		if rows.Scan(&c.ID, &c.Kind, &c.CommonName, &c.Label, &c.UserID, &c.UserName, &c.Hostnames, &c.Serial, &notAfter, &created, &revoked) != nil {
			continue
		}
		c.KindLabel = pkiKinds[c.Kind]
		c.NotAfter = notAfter.Local().Format("02.01.2006")
		c.Created = created.Local().Format("02.01.2006")
		c.Expired = notAfter.Before(now)
		if revoked != nil {
			c.Revoked, c.RevokedAt = true, revoked.Local().Format("02.01.2006")
		}
		out = append(out, c)
	}
	return out
}

// pkiRevealNew: Passwort des gerade ausgestellten Zertifikats in der Liste zeigen.
func (h *Handler) pkiRevealNew(ctx context.Context, list []pkiCertRow, newID string) {
	for i := range list {
		if list[i].ID == newID && list[i].Active() {
			list[i].Password, _ = h.pkiPasswordOf(ctx, newID)
		}
	}
}

func (h *Handler) pkiPasswordOf(ctx context.Context, id string) (string, error) {
	var enc string
	if err := h.db.QueryRow(ctx, `SELECT password_enc FROM pki_certificates WHERE id = $1::uuid`, id).Scan(&enc); err != nil {
		return "", err
	}
	return decryptSecret(h.pkiKey(), enc)
}

func pkiBack(w http.ResponseWriter, r *http.Request, base, newID, msg string, err error) {
	q := url.Values{}
	if err != nil {
		q.Set("err", err.Error())
	} else if msg != "" {
		q.Set("msg", msg)
	}
	if newID != "" {
		q.Set("new", newID)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	http.Redirect(w, r, base+sep+q.Encode(), http.StatusSeeOther)
}

// PKIPage: GET /core/settings/pki
func (h *Handler) PKIPage(w http.ResponseWriter, r *http.Request) {
	if !h.canPKI(r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	d := PKIPageData{
		BaseData: h.baseData(r, "pki", "Zertifikate", "Nextcloud"),
		Message:  q.Get("msg"), Err: q.Get("err"), NewID: q.Get("new"),
		Users: h.userOptions(ctx), SelfService: h.pkiSelfService(ctx), PublicBase: h.passwordResetBaseURL(),
	}
	if ca, err := h.pkiLoadCA(ctx); err != nil {
		d.CAError = err.Error()
	} else {
		d.CAName, d.CAValid, d.CAPrint = ca.Cert.Subject.CommonName, ca.Cert.NotAfter.Local().Format("02.01.2006"), pkiFingerprint(ca.Cert)
	}
	d.Certs = h.pkiList(ctx, "")
	h.pkiRevealNew(ctx, d.Certs, d.NewID)
	h.render(w, "pki", d)
}

var pkiDaysRe = regexp.MustCompile(`^[0-9]{1,4}$`)

// PKIIssueWeb: POST /core/settings/pki/issue (kind=client|device|server)
func (h *Handler) PKIIssueWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canPKI(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	_ = r.ParseForm()
	back := "/core/settings/pki"
	req := pkiRequest{Kind: r.FormValue("kind"), Label: strings.TrimSpace(r.FormValue("label"))}
	if d := r.FormValue("days"); pkiDaysRe.MatchString(d) {
		req.Days, _ = strconv.Atoi(d)
	}
	switch req.Kind {
	case "client":
		u, err := h.users.GetByID(ctx, r.FormValue("user_id"))
		if err != nil || u == nil {
			pkiBack(w, r, back, "", "", errors.New("Bitte eine Person auswählen"))
			return
		}
		req.UserID, req.Email = u.ID, u.Email
		req.CommonName = strings.TrimSpace(u.FirstName + " " + u.LastName)
		if req.Label != "" {
			req.CommonName += " (" + req.Label + ")"
		}
	case "device":
		req.CommonName = req.Label
	case "server":
		hosts, err := pkiParseHostnames(r.FormValue("hostnames"))
		if err != nil {
			pkiBack(w, r, back, "", "", err)
			return
		}
		req.Hostnames = hosts
		if len(hosts) > 0 {
			req.CommonName = hosts[0]
		}
	}
	id, err := h.pkiIssueAndStore(ctx, req, getUser(r).ID)
	if err != nil {
		pkiBack(w, r, back, "", "", err)
		return
	}
	pkiBack(w, r, back, id, "Zertifikat ausgestellt – Passwort notieren und .p12 herunterladen", nil)
}

func (h *Handler) pkiIssueAndStore(ctx context.Context, req pkiRequest, actorID string) (string, error) {
	ca, err := h.pkiLoadCA(ctx)
	if err != nil {
		return "", err
	}
	is, err := pkiIssue(ca, req, time.Now())
	if err != nil {
		return "", err
	}
	id, err := h.pkiStore(ctx, req, is, actorID)
	if err != nil {
		return "", err
	}
	componentLog("zertifikate").Info().Str("art", req.Kind).Str("name", req.CommonName).Str("serial", is.Serial).Str("von", actorID).Msg("ausgestellt")
	if req.UserID != "" {
		h.addHistory(ctx, "user", req.UserID, "update", "certificate", "", req.CommonName, "Zertifikat für die Nextcloud-App ausgestellt (gültig bis "+is.NotAfter.Local().Format("02.01.2006")+")", actorID)
	}
	return id, nil
}

// PKISelfServiceWeb: POST /core/settings/pki/self-service (on=1|0)
func (h *Handler) PKISelfServiceWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canPKI(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	v := "0"
	if r.FormValue("on") == "1" {
		v = "1"
	}
	err := h.setUpdateSetting(r.Context(), pkiSelfServiceSetting, v)
	pkiBack(w, r, "/core/settings/pki", "", "Selbstbedienung gespeichert", err)
}

// AccountCertificateWeb: POST /account/certificates – eigenes Zertifikat für die Nextcloud-App.
func (h *Handler) AccountCertificateWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := getUser(r)
	back := "/users/" + me.ID + "?tab=account"
	if me.IsSystemUser || (!h.pkiSelfService(ctx) && !h.canPKI(r)) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	var active int
	_ = h.db.QueryRow(ctx, `SELECT COUNT(*) FROM pki_certificates WHERE user_id = $1::uuid AND revoked_at IS NULL AND not_after > NOW()`, me.ID).Scan(&active)
	if active >= pkiSelfMax {
		pkiBack(w, r, back, "", "", fmt.Errorf("Höchstens %d gültige Zertifikate – zuerst ein altes sperren", pkiSelfMax))
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if len([]rune(label)) > 60 {
		label = string([]rune(label)[:60])
	}
	cn := strings.TrimSpace(me.FirstName + " " + me.LastName)
	if label != "" {
		cn += " (" + label + ")"
	}
	id, err := h.pkiIssueAndStore(ctx, pkiRequest{Kind: "client", CommonName: cn, Label: label, UserID: me.ID, Email: me.Email, Days: 730}, me.ID)
	pkiBack(w, r, back, id, "Zertifikat ausgestellt – Passwort notieren und .p12 herunterladen", err)
}

// pkiCertFor: Zertifikat laden, wenn es der Person gehört oder sie Zertifikate verwaltet.
func (h *Handler) pkiCertFor(w http.ResponseWriter, r *http.Request) (id, kind, cn string, ok bool) {
	id = chi.URLParam(r, "id")
	if !uuidInPathRe.MatchString(id) {
		http.NotFound(w, r)
		return "", "", "", false
	}
	var owner string
	if err := h.db.QueryRow(r.Context(), `SELECT kind, common_name, COALESCE(user_id::text,'') FROM pki_certificates WHERE id = $1::uuid`, id).Scan(&kind, &cn, &owner); err != nil {
		http.NotFound(w, r)
		return "", "", "", false
	}
	if !h.canPKI(r) && (owner == "" || owner != getUser(r).ID) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return "", "", "", false
	}
	return id, kind, cn, true
}

func pkiFileName(cn, ext string) string {
	s := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '_'
	}, cn)
	return strings.Trim(s, "_") + ext
}

func pkiDownload(w http.ResponseWriter, name, ctype string, data []byte) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// CertificateFileWeb: GET /certificates/{id}/{file} – p12, cert.pem, key.pem (nur Server-Zertifikate)
func (h *Handler) CertificateFileWeb(w http.ResponseWriter, r *http.Request) {
	id, kind, cn, ok := h.pkiCertFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var certPEM, keyEnc, p12Enc string
	var revoked bool
	if err := h.db.QueryRow(ctx, `SELECT cert_pem, key_enc, p12_enc, revoked_at IS NOT NULL FROM pki_certificates WHERE id = $1::uuid`, id).Scan(&certPEM, &keyEnc, &p12Enc, &revoked); err != nil {
		http.NotFound(w, r)
		return
	}
	file := chi.URLParam(r, "file")
	if revoked && file != "cert.pem" {
		http.Error(w, "Zertifikat ist gesperrt", http.StatusGone)
		return
	}
	switch file {
	case "p12":
		b64, err := decryptSecret(h.pkiKey(), p12Enc)
		data, err2 := base64.StdEncoding.DecodeString(b64)
		if err != nil || err2 != nil {
			http.Error(w, "Datei nicht lesbar – Schlüssel geändert?", http.StatusInternalServerError)
			return
		}
		pkiDownload(w, pkiFileName(cn, ".p12"), "application/x-pkcs12", data)
	case "cert.pem":
		pkiDownload(w, pkiFileName(cn, ".crt"), "application/x-pem-file", []byte(certPEM))
	case "fullchain.pem":
		ca, err := h.pkiLoadCA(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pkiDownload(w, pkiFileName(cn, "-fullchain.pem"), "application/x-pem-file", append([]byte(certPEM), ca.CertPEM...))
	case "key.pem":
		if kind != "server" || !h.canPKI(r) {
			http.Error(w, "nur für Server-Zertifikate", http.StatusForbidden)
			return
		}
		key, err := decryptSecret(h.pkiKey(), keyEnc)
		if err != nil || key == "" {
			http.Error(w, "Schlüssel nicht lesbar", http.StatusInternalServerError)
			return
		}
		pkiDownload(w, pkiFileName(cn, ".key"), "application/x-pem-file", []byte(key))
	default:
		http.NotFound(w, r)
	}
}

// CertificatePasswordWeb: GET /certificates/{id}/password (htmx)
func (h *Handler) CertificatePasswordWeb(w http.ResponseWriter, r *http.Request) {
	id, _, _, ok := h.pkiCertFor(w, r)
	if !ok {
		return
	}
	pw, err := h.pkiPasswordOf(r.Context(), id)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		_, _ = w.Write([]byte(`<span style="color:var(--red)">nicht lesbar</span>`))
		return
	}
	_, _ = w.Write([]byte(`<code style="user-select:all;font-size:13px">` + html.EscapeString(pw) + `</code>`))
}

// CertificateRevokeWeb: POST /certificates/{id}/revoke
func (h *Handler) CertificateRevokeWeb(w http.ResponseWriter, r *http.Request) {
	id, _, cn, ok := h.pkiCertFor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var owner string
	_ = h.db.QueryRow(ctx, `UPDATE pki_certificates SET revoked_at = NOW(), revoked_by = $2::uuid, p12_enc = '', password_enc = '', key_enc = ''
		WHERE id = $1::uuid AND revoked_at IS NULL RETURNING COALESCE(user_id::text,'')`, id, getUser(r).ID).Scan(&owner)
	componentLog("zertifikate").Info().Str("id", id).Str("name", cn).Str("von", getUser(r).ID).Msg("gesperrt")
	if owner != "" {
		h.addHistory(ctx, "user", owner, "update", "certificate", cn, "", "Zertifikat für die Nextcloud-App gesperrt", getUser(r).ID)
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/core/settings/pki"
	}
	pkiBack(w, r, back, "", "Zertifikat gesperrt – nginx/Cloudflare lehnen es ab, sobald die Sperrliste abgeholt ist", nil)
}

// PKICACertWeb: GET /pki/ca.crt[?format=der] – öffentlich (Geräte, nginx).
func (h *Handler) PKICACertWeb(w http.ResponseWriter, r *http.Request) {
	ca, err := h.pkiLoadCA(r.Context())
	if err != nil {
		http.Error(w, "Zertifizierungsstelle nicht verfügbar", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Query().Get("format") == "der" {
		b, _ := pem.Decode(ca.CertPEM)
		pkiDownload(w, "pdh-ca.cer", "application/pkix-cert", b.Bytes)
		return
	}
	pkiDownload(w, "pdh-ca.crt", "application/x-x509-ca-cert", ca.CertPEM)
}

// PKICRLWeb: GET /pki/crl.pem – öffentlich (nginx ssl_crl).
func (h *Handler) PKICRLWeb(w http.ResponseWriter, r *http.Request) {
	crl, err := h.pkiCRL(r.Context())
	if err != nil {
		http.Error(w, "Sperrliste nicht verfügbar", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	pkiDownload(w, "pdh-crl.pem", "application/x-pem-file", crl)
}
