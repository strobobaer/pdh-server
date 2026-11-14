package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Zertifikatspflicht: Fragt der Nextcloud-Server überhaupt nach Client-Zertifikaten – und
// nennt er die PDH-CA unter den angenommenen? Das PDH baut dazu eine TLS-Verbindung zur
// Nextcloud-Adresse auf (PDH_NEXTCLOUD_BASEURL), liest nur die Zertifikatsanforderung des
// Servers und sendet kein Zertifikat. Es wird nichts geschrieben; die Prüfung läuft nur,
// wenn die Anzeige geladen wird, und das Ergebnis bleibt einige Minuten im Speicher.

const (
	pkiEnforceActive  = "aktiv"
	pkiEnforceOff     = "aus"
	pkiEnforceUnknown = "unklar"

	pkiProbeTimeout = 3 * time.Second
	pkiProbeTTL     = 5 * time.Minute

	pkiEnforceCmd = "sudo bash scripts/configure-nextcloud-client-certs.sh --site /etc/nginx/sites-available/nextcloud.conf"
)

type pkiEnforceStatus struct {
	State  string // pkiEnforceActive | pkiEnforceOff | pkiEnforceUnknown
	Reason string // Grund bei „aus“ (Detail) und „unklar“
	Host   string
}

// pkiNextcloudAddr: Host und Port (Standard 443) aus PDH_NEXTCLOUD_BASEURL.
func pkiNextcloudAddr(baseURL string) (host, port string, err error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", "", errors.New("Nextcloud ist nicht eingerichtet")
	}
	u, perr := url.Parse(baseURL)
	if perr != nil || u.Hostname() == "" {
		return "", "", errors.New("Nextcloud-Adresse nicht lesbar")
	}
	port = u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			return u.Hostname(), "", errors.New("Nextcloud wird ohne TLS angesprochen (http)")
		}
	}
	return u.Hostname(), port, nil
}

// pkiProbe: TLS-Verbindung zu addr; caSubject = RawSubject der PDH-CA.
func pkiProbe(ctx context.Context, addr string, caSubject []byte) pkiEnforceStatus {
	ctx, cancel := context.WithTimeout(ctx, pkiProbeTimeout)
	defer cancel()
	var asked, listed bool
	var others int
	cfg := &tls.Config{
		// Nur die Zertifikatsanforderung wird gelesen – es fließen keine Daten.
		InsecureSkipVerify: true, //nolint:gosec
		GetClientCertificate: func(info *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			asked = true
			others = len(info.AcceptableCAs)
			if others == 0 {
				listed = true // keine Einschränkung: jede CA wird angenommen
			}
			for _, ca := range info.AcceptableCAs {
				if bytes.Equal(ca, caSubject) {
					listed = true
				}
			}
			return &tls.Certificate{}, nil
		},
	}
	host, _, _ := net.SplitHostPort(addr)
	cfg.ServerName = host
	d := &tls.Dialer{Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if conn != nil {
		_ = conn.Close()
	}
	switch {
	case asked && listed:
		return pkiEnforceStatus{State: pkiEnforceActive}
	case asked:
		return pkiEnforceStatus{State: pkiEnforceOff, Reason: "Der Server fragt zwar nach Zertifikaten, nennt die PDH-Zertifizierungsstelle aber nicht."}
	case err != nil:
		return pkiEnforceStatus{State: pkiEnforceUnknown, Reason: pkiProbeReason(err)}
	}
	return pkiEnforceStatus{State: pkiEnforceOff}
}

func pkiProbeReason(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "Nextcloud antwortet nicht innerhalb von 3 Sekunden."
	case strings.Contains(err.Error(), "refused"):
		return "Nextcloud ist unter dieser Adresse nicht erreichbar (Verbindung abgelehnt)."
	case strings.Contains(err.Error(), "no such host") || strings.Contains(err.Error(), "lookup"):
		return "Die Nextcloud-Adresse ist im Netz nicht auffindbar."
	}
	return "Nextcloud ist nicht erreichbar oder spricht kein TLS."
}

var pkiProbeCache = struct {
	sync.Mutex
	key  string
	at   time.Time
	stat pkiEnforceStatus
}{}

// pkiEnforcement: Ergebnis der Prüfung (einige Minuten zwischengespeichert).
func (h *Handler) pkiEnforcement(ctx context.Context) pkiEnforceStatus {
	return pkiEnforcementFor(ctx, os.Getenv("PDH_NEXTCLOUD_BASEURL"), func() ([]byte, error) {
		ca, err := h.pkiLoadCA(ctx)
		if err != nil {
			return nil, err
		}
		return ca.Cert.RawSubject, nil
	})
}

func pkiEnforcementFor(ctx context.Context, baseURL string, caSubject func() ([]byte, error)) pkiEnforceStatus {
	host, port, err := pkiNextcloudAddr(baseURL)
	if err != nil {
		return pkiEnforceStatus{State: pkiEnforceUnknown, Reason: err.Error(), Host: host}
	}
	subj, err := caSubject()
	if err != nil {
		return pkiEnforceStatus{State: pkiEnforceUnknown, Reason: "Die PDH-Zertifizierungsstelle ist nicht lesbar.", Host: host}
	}
	addr := net.JoinHostPort(host, port)
	key := addr + "|" + string(subj)
	pkiProbeCache.Lock()
	if pkiProbeCache.key == key && time.Since(pkiProbeCache.at) < pkiProbeTTL {
		st := pkiProbeCache.stat
		pkiProbeCache.Unlock()
		return st
	}
	pkiProbeCache.Unlock()
	st := pkiProbe(ctx, addr, subj)
	st.Host = host
	pkiProbeCache.Lock()
	pkiProbeCache.key, pkiProbeCache.at, pkiProbeCache.stat = key, time.Now(), st
	pkiProbeCache.Unlock()
	return st
}

// pkiEnforceHTML: Anzeige der Zertifikatspflicht (Farben nur über Variablen).
func pkiEnforceHTML(st pkiEnforceStatus) string {
	box := func(color, icon, title, body string) string {
		return `<div class="pki-enforce" style="border:1px solid var(--border);border-left:4px solid ` + color + `;background:var(--bg2);border-radius:8px;padding:9px 12px;font-size:12px;margin:0 0 12px">` +
			`<b style="color:` + color + `"><i class="ti ` + icon + `"></i> ` + title + `</b>` + body + `</div>`
	}
	switch st.State {
	case pkiEnforceActive:
		return box("var(--green)", "ti-shield-check", "Zertifikatspflicht aktiv – nginx fragt nach PDH-Zertifikaten", "")
	case pkiEnforceOff:
		body := ""
		if st.Reason != "" {
			body += `<div class="rec-sub" style="margin-top:4px">` + html.EscapeString(st.Reason) + `</div>`
		}
		body += `<div style="margin-top:6px">Auf dem Nextcloud-Server einmalig ausführen:</div>` +
			`<code style="display:block;user-select:all;margin-top:4px;padding:6px 8px;background:var(--bg3);border-radius:6px;word-break:break-all">` + html.EscapeString(pkiEnforceCmd) + `</code>` +
			`<div class="rec-sub" style="margin-top:6px">Im Verbund muss der Befehl auf beiden Servern laufen, und die eigenen Serveradressen sind mit <code>--exempt</code> auszunehmen.</div>`
		return box("var(--amber)", "ti-shield-off", "Zertifikatspflicht nicht eingerichtet – Nextcloud fragt kein Zertifikat ab; die .p12 hat so keine Wirkung", body)
	}
	reason := st.Reason
	if reason == "" {
		reason = "Die Prüfung war nicht möglich."
	}
	return box("var(--muted)", "ti-shield-question", "Zertifikatspflicht nicht prüfbar", `<div class="rec-sub" style="margin-top:4px">`+html.EscapeString(reason)+`</div>`)
}

// PKIEnforcementWeb: GET /account/certificates/enforcement – Anzeige als Fragment (htmx, lädt nach).
// Nur zur Anzeige, daher dieselbe Sichtbarkeit wie die Karten: Zertifikatsverwaltung oder Selbstbedienung.
func (h *Handler) PKIEnforcementWeb(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !h.canPKI(r) && !h.pkiSelfService(r.Context()) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	_, _ = fmt.Fprint(w, pkiEnforceHTML(h.pkiEnforcement(r.Context())))
}

// pkiServerHint: Hinweis zu „Server (Nextcloud)“ – das öffentliche Zertifikat kommt meist
// von Let's Encrypt (Reiter Cloudflare-Tunnel, certRole „nextcloud“).
func pkiServerHint(nextcloudURL string, rows []certRow) string {
	u, err := url.Parse(strings.TrimSpace(nextcloudURL))
	host := ""
	if err == nil {
		host = u.Hostname()
	}
	for _, r := range rows {
		if certHasCert(r) && !r.Staging && certRole(r.Name, "", nextcloudURL) == "nextcloud" {
			return "Nextcloud nutzt bereits das Let's-Encrypt-Zertifikat für " + r.Name + "."
		}
	}
	if host == "" {
		host = "der Nextcloud"
	}
	return "Für die Adresse " + host + " gilt das Let's-Encrypt-Zertifikat aus dem Reiter Cloudflare-Tunnel. Ein Server-Zertifikat aus dem PDH brauchst du nur für interne Namen ohne öffentliches Zertifikat (z. B. nextcloud.local oder eine IP)."
}
