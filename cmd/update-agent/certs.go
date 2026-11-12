package main

import (
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Zertifikate (Let's Encrypt): Das PDH holt das Zertifikat und übergibt es
// hier. Der Agent legt die Dateien unter /etc/ssl/pdh/<name>/ ab, schreibt die
// nginx-Seite des PDH bzw. tauscht in der Nextcloud-Seite nur die beiden
// Zertifikatszeilen, prüft mit „nginx -t“, lädt nginx neu und nimmt bei einem
// Fehler alles wieder zurück. Ohne Zertifikate läuft hier nichts: Der Agent
// startet unverändert, nur GET /v1/certs/status antwortet (ohne nginx leer).

const (
	certRootDir    = "/etc/ssl/pdh"
	nginxEtcDir    = "/etc/nginx"
	certPDHSite    = "pdh.conf"
	certSiteHeader = "# Vom PDH-Update-Agent geschrieben"
	certSwapError  = "Nextcloud-Seite hat nicht genau ein Zertifikat – nicht geändert"
)

// certRuntime hält die Haken des Agenten für Zertifikate. Leer = Betrieb auf dem
// Host (Pfade über /proc/1/root, Befehle über nsenter); Tests ersetzen die Felder.
type certRuntime struct {
	mu       sync.Mutex
	fs       func(p string) string                                                  // nil = a.hp (Host-Pfad)
	run      func(ctx context.Context, name string, args ...string) (string, error) // nil = Befehl im Host-Namensraum
	hasNginx func() bool                                                            // nil = nginx-Programm im Host vorhanden
	chgrp    func(path, group string) error                                         // nil = Gruppe setzen, soweit es sie gibt
}

func (a *updateAgent) certPath(p string) string {
	if a.certs.fs != nil {
		return a.certs.fs(p)
	}
	return a.hp(p)
}

func (a *updateAgent) certRun(ctx context.Context, name string, args ...string) (string, error) {
	if a.certs.run != nil {
		return a.certs.run(ctx, name, args...)
	}
	full := append([]string{"-t", "1", "-m", "-u", "-i", "-n", "--", name}, args...)
	out, err := exec.CommandContext(ctx, "nsenter", full...).CombinedOutput()
	return string(out), err
}

func (a *updateAgent) certNginxPresent() bool {
	if a.certs.hasNginx != nil {
		return a.certs.hasNginx()
	}
	for _, p := range []string{"/usr/sbin/nginx", "/usr/bin/nginx", "/usr/local/sbin/nginx", "/usr/local/bin/nginx"} {
		if st, err := os.Stat(a.certPath(p)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// certLayout: nginxLayout auf den Dateien des Hosts.
func (a *updateAgent) certLayout() (sitesDir, group string) {
	readFile := func(p string) ([]byte, error) { return os.ReadFile(a.certPath(p)) }
	exists := func(p string) bool { _, err := os.Stat(a.certPath(p)); return err == nil }
	return nginxLayout(readFile, exists)
}

func (a *updateAgent) certChgrp(path, group string) error {
	if a.certs.chgrp != nil {
		return a.certs.chgrp(path, group)
	}
	if group == "" {
		return nil
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return nil // Gruppe gibt es nicht: root liest die Dateien ohnehin (nginx-Hauptprozess)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return nil
	}
	return os.Chown(path, 0, gid)
}

var certLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// certValidName: nur ein Rechnername in Kleinbuchstaben (Punkte trennen die
// Teile). Damit kann der Name nie aus dem Ordner /etc/ssl/pdh ausbrechen und
// nie Zeichen enthalten, die in der nginx-Seite etwas bedeuten.
func certValidName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !certLabelRe.MatchString(label) {
			return false
		}
	}
	return true
}

var certUpstreamRe = regexp.MustCompile(`^http://(127\.0\.0\.1|localhost):([0-9]{1,5})$`)

// certValidUpstream: das PDH läuft immer auf demselben Rechner; nur diese
// Form darf in die nginx-Seite geschrieben werden.
func certValidUpstream(s string) bool {
	m := certUpstreamRe.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	port, err := strconv.Atoi(m[2])
	return err == nil && port >= 1 && port <= 65535
}

var certAbsPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// certValidConfPath: ein absoluter Pfad ohne Sonderzeichen und ohne „..“, wie er
// in eine ssl_certificate-Zeile gehört.
func certValidConfPath(s string) bool {
	if !certAbsPathRe.MatchString(s) || len(s) > 400 {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func certDir(name string) string       { return certRootDir + "/" + name }
func certChainPath(name string) string { return certDir(name) + "/fullchain.pem" }
func certKeyPath(name string) string   { return certDir(name) + "/privkey.pem" }

// certSiteConf: nginx-Seite für den PDH-Namen – 443 mit dem Zertifikat und
// Weiterreichen an das PDH, 80 leitet um.
func certSiteConf(name, upstream string) string {
	var b strings.Builder
	b.WriteString(certSiteHeader + " (Zertifikate). Änderungen gehen beim nächsten Zertifikatslauf verloren.\n")
	b.WriteString("server {\n")
	b.WriteString("    listen 80;\n")
	b.WriteString("    server_name " + name + ";\n")
	b.WriteString("    return 301 https://$host$request_uri;\n")
	b.WriteString("}\n\n")
	b.WriteString("server {\n")
	b.WriteString("    listen 443 ssl;\n")
	b.WriteString("    server_name " + name + ";\n\n")
	b.WriteString("    ssl_certificate " + certChainPath(name) + ";\n")
	b.WriteString("    ssl_certificate_key " + certKeyPath(name) + ";\n")
	b.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n\n")
	b.WriteString("    client_max_body_size 0;\n\n")
	b.WriteString("    location / {\n")
	b.WriteString("        proxy_pass " + upstream + ";\n")
	b.WriteString("        proxy_http_version 1.1;\n")
	b.WriteString("        proxy_set_header Host $host;\n")
	b.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
	b.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
	b.WriteString("        proxy_set_header X-Forwarded-Proto https;\n")
	b.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
	b.WriteString("        proxy_set_header Connection \"upgrade\";\n")
	b.WriteString("        proxy_buffering off;\n")
	b.WriteString("        proxy_read_timeout 3600s;\n")
	b.WriteString("    }\n")
	b.WriteString("}\n")
	return b.String()
}

var (
	certLineRe      = regexp.MustCompile(`(?m)^([ \t]*ssl_certificate[ \t]+)([^;\s]+)([ \t]*;)`)
	certKeyLineRe   = regexp.MustCompile(`(?m)^([ \t]*ssl_certificate_key[ \t]+)([^;\s]+)([ \t]*;)`)
	certServerRe    = regexp.MustCompile(`(?m)^[ \t]*server[ \t]*\{`)
	certServerNameR = regexp.MustCompile(`(?m)^[ \t]*server_name[ \t]+([^;]+);`)
)

// certCurrentPaths nennt die Zertifikatspfade einer Nextcloud-Seite mit genau
// einem Paar; sonst den Fehler von certSwapLines.
func certCurrentPaths(conf string) (cert, key string, err error) {
	c := certLineRe.FindAllStringSubmatch(conf, -1)
	k := certKeyLineRe.FindAllStringSubmatch(conf, -1)
	if len(c) != 1 || len(k) != 1 || len(certServerRe.FindAllString(conf, -1)) > 1 {
		return "", "", errors.New(certSwapError)
	}
	return c[0][2], k[0][2], nil
}

// certSwapLines ersetzt in einer Nextcloud-Seite nur die beiden Pfade der
// Zertifikatszeilen; alles andere bleibt byte-gleich. Gibt es nicht genau ein
// Paar (oder mehrere server-Blöcke), kommt der Fehler und die Seite unverändert zurück.
func certSwapLines(conf, certPath, keyPath string) (string, error) {
	if _, _, err := certCurrentPaths(conf); err != nil {
		return conf, err
	}
	out := certLineRe.ReplaceAllString(conf, "${1}"+certPath+"${3}")
	out = certKeyLineRe.ReplaceAllString(out, "${1}"+keyPath+"${3}")
	return out, nil
}

var certUserRe = regexp.MustCompile(`(?m)^[ \t]*user[ \t]+([A-Za-z0-9_.-]+)(?:[ \t]+[A-Za-z0-9_.-]+)?[ \t]*;`)

// nginxLayout liest, wo nginx seine Seiten sucht und unter welchem Benutzer es
// läuft: Debian/Ubuntu → sites-enabled, sonst conf.d. Die Gruppe ist der
// Benutzer aus nginx.conf, sonst der übliche Name der Verteilung.
func nginxLayout(readFile func(string) ([]byte, error), exists func(string) bool) (sitesDir, group string) {
	sitesDir = nginxEtcDir + "/conf.d"
	group = "nginx"
	if exists(nginxEtcDir + "/sites-enabled") {
		sitesDir = nginxEtcDir + "/sites-enabled"
		group = "www-data"
	}
	if data, err := readFile(nginxEtcDir + "/nginx.conf"); err == nil {
		if m := certUserRe.FindSubmatch(data); m != nil {
			group = string(m[1])
		}
	}
	return sitesDir, group
}

// certParse prüft Kette und Schlüssel, bevor etwas geschrieben wird, und
// liefert den Fingerabdruck (SHA-256 der DER-Form, Kleinbuchstaben-Hex) des
// ersten Zertifikats. Die Fehlertexte enthalten nie PEM.
func certParse(chainPEM, keyPEM string) (fingerprint string, err error) {
	rest := []byte(chainPEM)
	var leaf *x509.Certificate
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			return "", errors.New("Zertifikatskette enthält etwas anderes als Zertifikate")
		}
		c, perr := x509.ParseCertificate(blk.Bytes)
		if perr != nil {
			return "", errors.New("Zertifikat in der Kette ist nicht lesbar")
		}
		if leaf == nil {
			leaf = c
		}
	}
	if leaf == nil || len(strings.TrimSpace(string(rest))) > 0 {
		return "", errors.New("Zertifikatskette ist leer oder nicht lesbar")
	}
	kb, krest := pem.Decode([]byte(keyPEM))
	if kb == nil || len(strings.TrimSpace(string(krest))) > 0 {
		return "", errors.New("Schlüssel ist leer oder nicht lesbar")
	}
	var key any
	var kerr error
	switch kb.Type {
	case "PRIVATE KEY":
		key, kerr = x509.ParsePKCS8PrivateKey(kb.Bytes)
	case "EC PRIVATE KEY":
		key, kerr = x509.ParseECPrivateKey(kb.Bytes)
	case "RSA PRIVATE KEY":
		key, kerr = x509.ParsePKCS1PrivateKey(kb.Bytes)
	default:
		kerr = errors.New("unbekannte Schlüsselart")
	}
	if kerr != nil {
		return "", errors.New("Schlüssel ist nicht lesbar")
	}
	signer, ok := key.(interface{ Public() crypto.PublicKey })
	pub, ok2 := leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !ok2 || !pub.Equal(signer.Public()) {
		return "", errors.New("Schlüssel passt nicht zum Zertifikat")
	}
	sum := sha256.Sum256(leaf.Raw)
	return hex.EncodeToString(sum[:]), nil
}

// certScrub kürzt die Ausgabe eines Befehls für die Antwort; Zeilen, die nach
// Schlüsselmaterial aussehen, fallen weg.
func certScrub(s string) string {
	var keep []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.Contains(l, "-----") || strings.Contains(l, "PRIVATE KEY") {
			continue
		}
		keep = append(keep, l)
	}
	out := strings.TrimSpace(strings.Join(keep, "\n"))
	if len(out) > 2000 {
		out = out[:2000] + " …"
	}
	return out
}

// certBackup hält den Stand einer Datei im Speicher, um ihn wiederherzustellen;
// eine Datei, die es noch nicht gab, wird beim Zurücknehmen wieder entfernt.
type certBackup struct {
	path    string
	data    []byte
	mode    os.FileMode
	existed bool
}

func certTake(path string) (certBackup, error) {
	b := certBackup{path: path}
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return b, err
	}
	b.data, b.mode, b.existed = data, st.Mode().Perm(), true
	return b, nil
}

func (b certBackup) restore() error {
	if !b.existed {
		if err := os.Remove(b.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.WriteFile(b.path, b.data, b.mode); err != nil {
		return err
	}
	return os.Chmod(b.path, b.mode)
}

// certWriteAtomic schreibt in eine Nachbardatei (*.neu) und benennt um.
func (a *updateAgent) certWriteAtomic(path string, data []byte, mode os.FileMode, group string) error {
	tmp := path + ".neu"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fail(err)
	}
	if err := a.certChgrp(tmp, group); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return nil
}

type certRequest struct {
	Name        string `json:"name"`
	ChainPEM    string `json:"chain_pem"`
	KeyPEM      string `json:"key_pem"`
	Role        string `json:"role"` // pdh | nextcloud | "" (nur Dateien)
	Upstream    string `json:"upstream"`
	RestoreCert string `json:"restore_cert"`
	RestoreKey  string `json:"restore_key"`
}

func certFail(w http.ResponseWriter, status int, msg string) {
	writeMountJSON(w, status, map[string]any{"ok": false, "error": msg})
}

// certResolve folgt Verknüpfungen einer nginx-Seite. Absolute Ziele gelten im
// Host (nicht im Namensraum des Agenten, sonst landet ein Schreibzugriff auf
// dessen schreibgeschütztem /etc); relative Ziele gelten ab dem Ordner der Verknüpfung.
func (a *updateAgent) certResolve(p string) (string, error) {
	for i := 0; i < 8; i++ {
		st, err := os.Lstat(p)
		if err != nil {
			if i == 0 && errors.Is(err, os.ErrNotExist) {
				return p, nil // am Pfad selbst liegt nichts
			}
			return "", err // Verknüpfung ins Leere oder nicht zugänglich
		}
		if st.Mode()&os.ModeSymlink == 0 {
			return p, nil
		}
		t, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(t, "/") || filepath.IsAbs(t) {
			p = a.certPath(filepath.ToSlash(t))
		} else {
			p = filepath.Join(filepath.Dir(p), t)
		}
	}
	return "", errors.New("zu viele Verknüpfungen")
}

type certSite struct {
	entry string // Dateiname im Seiten-Ordner
	path  string // aufgelöste Datei, in die geschrieben wird
}

// certSitesWithName nennt alle Seiten im Seiten-Ordner (außer pdh.conf), deren
// server_name den Namen enthält.
func (a *updateAgent) certSitesWithName(sitesDir, name string) ([]certSite, error) {
	entries, err := os.ReadDir(a.certPath(sitesDir))
	if err != nil {
		return nil, errors.New("nginx-Seiten sind nicht lesbar")
	}
	var found []certSite
	for _, e := range entries {
		if e.Name() == certPDHSite || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, err := a.certResolve(a.certPath(sitesDir + "/" + e.Name()))
		if err != nil {
			continue
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, m := range certServerNameR.FindAllStringSubmatch(string(data), -1) {
			hit := false
			for _, f := range strings.Fields(m[1]) {
				if f == name {
					hit = true
				}
			}
			if hit {
				found = append(found, certSite{entry: e.Name(), path: p})
				break
			}
		}
	}
	return found, nil
}

// certFindNextcloudSite sucht die eine Nextcloud-Seite (aufgelöster Pfad).
func (a *updateAgent) certFindNextcloudSite(sitesDir, name string) (string, error) {
	found, err := a.certSitesWithName(sitesDir, name)
	if err != nil {
		return "", err
	}
	switch len(found) {
	case 0:
		return "", errors.New("Keine Nextcloud-Seite mit diesem Namen gefunden – nicht geändert")
	case 1:
		return found[0].path, nil
	}
	return "", errors.New("Mehrere nginx-Seiten tragen diesen Namen – nicht geändert")
}

// certOwnSite prüft die PDH-Seite: target ist die Datei, in die geschrieben wird
// (hinter Verknüpfungen), exists/own sagen, ob es sie gibt und ob der Agent sie
// geschrieben hat. Nur wenn am Pfad wirklich nichts liegt, gilt sie als frei; jeder
// andere Lesefehler (Verknüpfung ins Leere, keine Rechte) ist ein Fehler, und die
// Seite darf dann nicht angefasst werden.
func (a *updateAgent) certOwnSite(path string) (target string, exists, own bool, err error) {
	target, err = a.certResolve(path)
	if err != nil {
		return "", false, false, err
	}
	data, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return target, false, true, nil
	}
	if err != nil {
		return "", false, false, err
	}
	return target, true, strings.HasPrefix(string(data), certSiteHeader), nil
}

// certRestoreAll stellt alle Sicherungen wieder her und nennt die Dateien, bei
// denen das nicht gelang (nur Dateinamen).
func certRestoreAll(backups []certBackup) []string {
	var failed []string
	for _, b := range backups {
		if err := b.restore(); err != nil {
			failed = append(failed, filepath.Base(b.path))
		}
	}
	return failed
}

// certRollbackText: Satz für die Antwort, ehrlich auch bei unvollständigem Rückweg.
func certRollbackText(failed []string) string {
	if len(failed) == 0 {
		return "alles zurückgenommen"
	}
	return "Zurücknehmen aber unvollständig, bitte prüfen: " + strings.Join(failed, ", ")
}

// certRollbackNote: Zusatz für Fehlertexte, wenn das Zurücknehmen nicht vollständig war.
func certRollbackNote(failed []string) string {
	if len(failed) == 0 {
		return ""
	}
	return " (Zurücknehmen unvollständig, bitte prüfen: " + strings.Join(failed, ", ") + ")"
}

// certApplyNginx prüft die Konfiguration und lädt nginx neu. Bei einem Fehler
// stellt rollback den vorigen Stand her; nginx läuft dann mit der alten Konfiguration weiter.
func (a *updateAgent) certApplyNginx(ctx context.Context, rollback func() []string) error {
	if out, err := a.certRun(ctx, "nginx", "-t"); err != nil {
		failed := rollback()
		text := certScrub(out)
		if text == "" {
			text = err.Error()
		}
		return fmt.Errorf("nginx -t meldet einen Fehler, %s: %s", certRollbackText(failed), text)
	}
	if out, err := a.certRun(ctx, "systemctl", "reload", "nginx"); err != nil {
		failed := rollback()
		_, _ = a.certRun(ctx, "systemctl", "reload", "nginx")
		text := certScrub(out)
		if text == "" {
			text = err.Error()
		}
		return fmt.Errorf("nginx lässt sich nicht neu laden, %s: %s", certRollbackText(failed), text)
	}
	return nil
}

func (a *updateAgent) certRead(w http.ResponseWriter, r *http.Request) (certRequest, bool) {
	var req certRequest
	if a.mode != "systemd" {
		certFail(w, http.StatusOK, "Zertifikate werden nur im systemd-Betrieb eingerichtet")
		return req, false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		certFail(w, http.StatusBadRequest, "ungültige Anfrage")
		return req, false
	}
	if !certValidName(req.Name) {
		certFail(w, http.StatusBadRequest, "ungültiger Name")
		return req, false
	}
	if req.Role != "" && req.Role != "pdh" && req.Role != "nextcloud" {
		certFail(w, http.StatusBadRequest, "unbekannte Rolle")
		return req, false
	}
	return req, true
}

// certInstallHandler: POST /v1/certs/install
func (a *updateAgent) certInstallHandler(w http.ResponseWriter, r *http.Request) {
	req, ok := a.certRead(w, r)
	if !ok {
		return
	}
	if req.Role == "pdh" && !certValidUpstream(req.Upstream) {
		certFail(w, http.StatusBadRequest, "ungültige Adresse des PDH (erwartet http://127.0.0.1:<Port>)")
		return
	}
	fingerprint, err := certParse(req.ChainPEM, req.KeyPEM)
	if err != nil {
		certFail(w, http.StatusBadRequest, err.Error())
		return
	}
	a.certs.mu.Lock()
	defer a.certs.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	// Rolle pdh/nextcloud braucht nginx. Rolle "" schreibt nur die Dateien; ist nginx
	// da, wird trotzdem geprüft und neu geladen (eine von Hand geschriebene Seite
	// übernimmt so das erneuerte Zertifikat), ohne nginx genügt das Ablegen.
	withNginx := a.certNginxPresent()
	if req.Role != "" && !withNginx {
		certFail(w, http.StatusOK, "nginx ist nicht eingerichtet")
		return
	}

	sitesDir, group := a.certLayout()
	var sitePath, siteData, prevCert, prevKey string
	switch req.Role {
	case "pdh":
		target, _, own, err := a.certOwnSite(a.certPath(sitesDir + "/" + certPDHSite))
		if err != nil || !own {
			certFail(w, http.StatusOK, "Die nginx-Seite "+certPDHSite+" stammt nicht vom Update-Agent oder ist nicht lesbar – nicht geändert")
			return
		}
		sitePath = target // hinter einer Verknüpfung: die Zieldatei, die Verknüpfung bleibt
		others, err := a.certSitesWithName(sitesDir, req.Name)
		if err != nil {
			certFail(w, http.StatusOK, err.Error())
			return
		}
		if len(others) > 0 {
			certFail(w, http.StatusOK, "Der Name wird schon von der nginx-Seite "+others[0].entry+" bedient – nicht geändert")
			return
		}
		siteData = certSiteConf(req.Name, req.Upstream)
	case "nextcloud":
		// Seite zuerst prüfen: Weicht sie ab, wird nichts angefasst.
		p, err := a.certFindNextcloudSite(sitesDir, req.Name)
		if err != nil {
			certFail(w, http.StatusOK, err.Error())
			return
		}
		old, err := os.ReadFile(p)
		if err != nil {
			certFail(w, http.StatusOK, "Nextcloud-Seite ist nicht lesbar")
			return
		}
		prevCert, prevKey, err = certCurrentPaths(string(old))
		if err != nil {
			certFail(w, http.StatusOK, err.Error())
			return
		}
		sw, err := certSwapLines(string(old), certChainPath(req.Name), certKeyPath(req.Name))
		if err != nil {
			certFail(w, http.StatusOK, err.Error())
			return
		}
		sitePath, siteData = p, sw
	}

	chainFile := a.certPath(certChainPath(req.Name))
	keyFile := a.certPath(certKeyPath(req.Name))
	dir := a.certPath(certDir(req.Name))
	dirExisted := false
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		dirExisted = true
	}
	rootExisted := false
	if st, err := os.Stat(a.certPath(certRootDir)); err == nil && st.IsDir() {
		rootExisted = true
	}
	var backups []certBackup
	paths := []string{chainFile, keyFile}
	if sitePath != "" {
		paths = append(paths, sitePath)
	}
	for _, p := range paths {
		b, err := certTake(p)
		if err != nil {
			certFail(w, http.StatusOK, "Bestehende Datei ist nicht lesbar: "+filepath.Base(p))
			return
		}
		backups = append(backups, b)
	}
	rollback := func() []string {
		failed := certRestoreAll(backups)
		if !dirExisted {
			_ = os.Remove(dir) // nur der selbst angelegte, nun leere Ordner
			if !rootExisted {
				_ = os.Remove(a.certPath(certRootDir))
			}
		}
		return failed
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		certFail(w, http.StatusOK, "Ordner für das Zertifikat lässt sich nicht anlegen"+certRollbackNote(rollback()))
		return
	}
	_ = os.Chmod(dir, 0o750)
	_ = a.certChgrp(dir, group)
	if err := a.certWriteAtomic(chainFile, []byte(req.ChainPEM), 0o644, group); err != nil {
		certFail(w, http.StatusOK, "Zertifikat lässt sich nicht schreiben"+certRollbackNote(rollback()))
		return
	}
	if err := a.certWriteAtomic(keyFile, []byte(req.KeyPEM), 0o640, group); err != nil {
		certFail(w, http.StatusOK, "Schlüssel lässt sich nicht schreiben"+certRollbackNote(rollback()))
		return
	}
	if sitePath != "" {
		if req.Role == "pdh" {
			err = a.certWriteAtomic(sitePath, []byte(siteData), 0o644, "")
		} else {
			// sitePath ist die aufgelöste Zieldatei; die Verknüpfung selbst bleibt.
			err = a.certWriteAtomic(sitePath, []byte(siteData), backups[2].mode, "")
		}
		if err != nil {
			certFail(w, http.StatusOK, "nginx-Seite lässt sich nicht schreiben"+certRollbackNote(rollback()))
			return
		}
	}
	if withNginx {
		if err := a.certApplyNginx(ctx, rollback); err != nil {
			certFail(w, http.StatusOK, err.Error())
			return
		}
	}
	resp := map[string]any{"ok": true, "fingerprint": fingerprint}
	// Vorige Pfade nur melden, wenn die Seite noch nicht auf unser Zertifikat zeigte.
	if req.Role == "nextcloud" && prevCert != certChainPath(req.Name) {
		resp["previous_cert"], resp["previous_key"] = prevCert, prevKey
	}
	writeMountJSON(w, http.StatusOK, resp)
}

// certRemoveHandler: POST /v1/certs/remove
func (a *updateAgent) certRemoveHandler(w http.ResponseWriter, r *http.Request) {
	req, ok := a.certRead(w, r)
	if !ok {
		return
	}
	if req.Role == "nextcloud" && (!certValidConfPath(req.RestoreCert) || !certValidConfPath(req.RestoreKey)) {
		certFail(w, http.StatusBadRequest, "Vorige Zertifikatspfade der Nextcloud-Seite fehlen oder sind ungültig")
		return
	}
	a.certs.mu.Lock()
	defer a.certs.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	withNginx := req.Role != "" && a.certNginxPresent()
	if withNginx {
		sitesDir, _ := a.certLayout()
		var backups []certBackup
		changed := false
		switch req.Role {
		case "pdh":
			p := a.certPath(sitesDir + "/" + certPDHSite)
			// Nur eine vom Agenten geschriebene Seite wird entfernt; eine eigene bleibt.
			// Eine Verknüpfung (target != p) bleibt ebenso unberührt wie ein Lesefehler.
			if target, exists, own, err := a.certOwnSite(p); err == nil && exists && own && target == p {
				b, err := certTake(p)
				if err != nil {
					certFail(w, http.StatusOK, "nginx-Seite ist nicht lesbar")
					return
				}
				backups = append(backups, b)
				if err := os.Remove(p); err != nil {
					certFail(w, http.StatusOK, "nginx-Seite lässt sich nicht entfernen")
					return
				}
				changed = true
			}
		case "nextcloud":
			p, err := a.certFindNextcloudSite(sitesDir, req.Name)
			if err != nil {
				certFail(w, http.StatusOK, err.Error())
				return
			}
			old, err := os.ReadFile(p)
			if err != nil {
				certFail(w, http.StatusOK, "Nextcloud-Seite ist nicht lesbar")
				return
			}
			sw, err := certSwapLines(string(old), req.RestoreCert, req.RestoreKey)
			if err != nil {
				certFail(w, http.StatusOK, err.Error())
				return
			}
			b, err := certTake(p)
			if err != nil {
				certFail(w, http.StatusOK, "Nextcloud-Seite ist nicht lesbar")
				return
			}
			backups = append(backups, b)
			if sw != string(old) {
				if err := a.certWriteAtomic(p, []byte(sw), b.mode, ""); err != nil {
					certFail(w, http.StatusOK, "Nextcloud-Seite lässt sich nicht schreiben"+certRollbackNote(certRestoreAll(backups)))
					return
				}
				changed = true
			}
		}
		if changed {
			rollback := func() []string { return certRestoreAll(backups) }
			if err := a.certApplyNginx(ctx, rollback); err != nil {
				certFail(w, http.StatusOK, err.Error())
				return
			}
		}
	}
	// Erst jetzt, wo nginx die Dateien nicht mehr braucht, verschwinden sie.
	if err := os.RemoveAll(a.certPath(certDir(req.Name))); err != nil {
		certFail(w, http.StatusOK, "Zertifikatsdateien lassen sich nicht löschen")
		return
	}
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type certStatusEntry struct {
	Name        string `json:"name"`
	NotAfter    string `json:"not_after"`
	Fingerprint string `json:"fingerprint"`
}

// certStatusHandler: GET /v1/certs/status – ohne nginx oder ohne Ordner eine
// leere Liste, kein Fehler.
func (a *updateAgent) certStatusHandler(w http.ResponseWriter, _ *http.Request) {
	certs := []certStatusEntry{}
	entries, err := os.ReadDir(a.certPath(certRootDir))
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() || !certValidName(e.Name()) {
				continue
			}
			data, err := os.ReadFile(a.certPath(certChainPath(e.Name())))
			if err != nil {
				continue
			}
			blk, _ := pem.Decode(data)
			if blk == nil || blk.Type != "CERTIFICATE" {
				continue
			}
			c, err := x509.ParseCertificate(blk.Bytes)
			if err != nil {
				continue
			}
			sum := sha256.Sum256(c.Raw)
			certs = append(certs, certStatusEntry{
				Name:        e.Name(),
				NotAfter:    c.NotAfter.UTC().Format(time.RFC3339),
				Fingerprint: hex.EncodeToString(sum[:]),
			})
		}
	}
	sort.Slice(certs, func(i, j int) bool { return certs[i].Name < certs[j].Name })
	writeMountJSON(w, http.StatusOK, map[string]any{"ok": true, "nginx": a.mode == "systemd" && a.certNginxPresent(), "certs": certs})
}
