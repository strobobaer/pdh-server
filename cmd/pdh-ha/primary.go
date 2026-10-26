package main

// Aktive Seite des Datenbank-Spiegels: Replikationsbenutzer, Zugänge und
// Einstellungen für den Partner einrichten und die PDH-Konfiguration über
// Port 7480 (mTLS, nur Knoten-Zertifikate) an ihn übergeben.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"pdh/internal/ha"
)

type pullResp struct {
	PGMajor     string `json:"pg_major"`
	DBBytes     int64  `json:"db_bytes"`
	ReplicaPass string `json:"replica_pass,omitempty"`
	Archive     []byte `json:"archive,omitempty"` // tar.gz: .env und etc/… (ohne ha/)
}

// pdhRepo / pdhEtc: Quelle der PDH-Konfiguration (Tests überschreiben).
func pdhRepo() string {
	if r := os.Getenv("PDH_REPO"); r != "" {
		return r
	}
	return "/opt/pdh-server"
}

func pdhEtc() string {
	if e := os.Getenv("PDH_HA_PDH_ETC"); e != "" {
		return e
	}
	return "/etc/pdh"
}

// buildPGEnv: was pgActor über Partner, Wege und Schlüssel wissen muss.
func buildPGEnv(self, partner clusterNode, cl clusterConfig, pass string, c pgCluster, dataCurrent bool) pgEnv {
	e := pgEnv{SelfID: self.ID, PartnerID: partner.ID, ReplicaPass: pass, DataCurrent: dataCurrent}
	s := cl.Settings
	if s.Validate() != nil {
		s = ha.DefaultSettings()
	}
	e.SlotKeepMB = s.SlotKeep >> 20
	for _, a := range self.Addrs {
		e.Listen = append(e.Listen, hostOfAddr(a))
	}
	for _, a := range partner.Addrs {
		e.PartnerAddrs = append(e.PartnerAddrs, hostOfAddr(a))
	}
	dir := path.Join(c.ConfDir, "pdh-ha")
	e.CAFile, e.CertFile, e.KeyFile = path.Join(dir, "ca.crt"), path.Join(dir, "node.crt"), path.Join(dir, "node.key")
	return e
}

// newReplicaPass: 32 Byte zufällig, hex (sicher in SQL und passfile).
func newReplicaPass() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// copyCerts legt die Kopien von Knotenzertifikat, Schlüssel und CA für
// PostgreSQL an (Eigentümer postgres, 0600).
func (a *pgActor) copyCerts(ctx context.Context, etcDir string, e pgEnv) error {
	for src, dst := range map[string]string{"node.crt": e.CertFile, "node.key": e.KeyFile, "ca.crt": e.CAFile} {
		b, err := os.ReadFile(filepath.Join(etcDir, src))
		if err != nil {
			return err
		}
		if err := a.pg.writeOwned(ctx, filepath.FromSlash(dst), b); err != nil {
			return err
		}
	}
	return nil
}

// writeAccess: passfile und pg_hba-Block für die Adressen des Partners.
func (a *pgActor) writeAccess(ctx context.Context, e pgEnv) error {
	if err := a.pg.writeOwned(ctx, filepath.FromSlash(a.passfile), []byte(fmt.Sprintf("*:%d:*:%s:%s\n", pgPort, replicaUser, e.ReplicaPass))); err != nil {
		return err
	}
	hbaPath := filepath.Join(filepath.FromSlash(a.pg.c.ConfDir), "pg_hba.conf")
	hba, err := os.ReadFile(hbaPath)
	if err != nil {
		return err
	}
	return a.pg.writeOwned(ctx, hbaPath, []byte(replaceHBABlock(string(hba), renderHBABlock(e.PartnerAddrs))))
}

// ensurePrimary richtet den Spiegel an der aktiven Seite ein (wiederholbar).
func (a *pgActor) ensurePrimary(ctx context.Context, etcDir string) error {
	e := a.env()
	if e.ReplicaPass == "" || e.PartnerID == "" {
		return errors.New("Replikationspasswort oder Partner fehlt")
	}
	if strings.Trim(e.ReplicaPass, "0123456789abcdef") != "" {
		return errors.New("Replikationspasswort ungültig")
	}
	sql := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
    CREATE ROLE %[1]s WITH REPLICATION LOGIN PASSWORD '%[2]s';
  ELSE
    ALTER ROLE %[1]s WITH REPLICATION LOGIN PASSWORD '%[2]s';
  END IF;
END $$;
GRANT EXECUTE ON FUNCTION pg_ls_dir(text, boolean, boolean) TO %[1]s;
GRANT EXECUTE ON FUNCTION pg_stat_file(text, boolean) TO %[1]s;
GRANT EXECUTE ON FUNCTION pg_read_binary_file(text) TO %[1]s;
GRANT EXECUTE ON FUNCTION pg_read_binary_file(text, bigint, bigint, boolean) TO %[1]s;`, replicaUser, e.ReplicaPass)
	if _, err := a.pg.query(ctx, sql); err != nil {
		return fmt.Errorf("Replikationsbenutzer nicht angelegt: %w", err)
	}
	if err := a.copyCerts(ctx, etcDir, e); err != nil {
		return fmt.Errorf("Zertifikate für PostgreSQL nicht kopiert: %w", err)
	}
	if err := a.writeAccess(ctx, e); err != nil {
		return err
	}
	if a.syncTarget() == "" {
		a.setSyncTarget(slotName(e.PartnerID))
	}
	if err := a.pg.writeConf(ctx, a.primaryConf(e, false)); err != nil {
		return err
	}
	if err := a.pg.ctl(ctx, "reload"); err != nil {
		return err
	}
	if n, err := a.pg.query(ctx, "SELECT count(*) FROM pg_settings WHERE pending_restart"); err == nil && n != "0" {
		if err := a.pg.ctl(ctx, "restart"); err != nil {
			return err
		}
		a.ev.add(ha.Event{Kind: "info", Text: "PostgreSQL neu gestartet (Einstellungen für den Spiegel)"})
	}
	slot := slotName(e.PartnerID)
	if _, err := a.pg.query(ctx, fmt.Sprintf(
		"SELECT pg_create_physical_replication_slot('%[1]s') WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = '%[1]s')", slot)); err != nil {
		return err
	}
	return a.svc.SetAutostart(ctx, false)
}

// configArchive packt .env des PDH und /etc/pdh ohne ha/ (tar.gz).
func configArchive(repo, etc string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, b []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(b)
		return err
	}
	if b, err := os.ReadFile(filepath.Join(repo, ".env")); err == nil {
		if err := add(".env", b); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	err := filepath.WalkDir(etc, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		rel, _ := filepath.Rel(etc, p)
		rel = filepath.ToSlash(rel)
		if de.IsDir() {
			if rel == "ha" {
				return filepath.SkipDir // knotenspezifisch, nie übertragen
			}
			return nil
		}
		if !de.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return add("etc/"+rel, b)
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// extractConfig spielt das Archiv ein: nur .env und Dateien unter etc/.
func extractConfig(archive []byte, repo, etc string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := h.Name
		if h.Typeflag != tar.TypeReg || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, `\`) {
			return fmt.Errorf("Ungültiger Eintrag im Konfigurationsarchiv: %q", name)
		}
		var dst string
		switch {
		case name == ".env":
			dst = filepath.Join(repo, ".env")
		case strings.HasPrefix(name, "etc/") && !strings.HasPrefix(name, "etc/ha/"):
			dst = filepath.Join(etc, filepath.FromSlash(strings.TrimPrefix(name, "etc/")))
		default:
			return fmt.Errorf("Ungültiger Eintrag im Konfigurationsarchiv: %q", name)
		}
		b, err := io.ReadAll(io.LimitReader(tr, 16<<20))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		if err := writeFileAtomic(dst, b, 0o640); err != nil {
			return err
		}
	}
}

// configPullHTTP: POST /v1/config/pull – nur Knoten des Verbunds mit
// gültigem Zertifikat, nur am aktiven Knoten.
func (d *daemon) configPullHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		http.Error(w, "Nur mit Knotenzertifikat", http.StatusForbidden)
		return
	}
	var req struct {
		Dry bool `json:"dry"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	resp, code, err := d.configPull(r.Context(), *r.TLS, req.Dry)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// configPull prüft den Anfragenden und liefert Hauptversion, Größe und – ohne
// dry – Passwort und Konfiguration; die aktive Seite wird dabei eingerichtet.
func (d *daemon) configPull(ctx context.Context, cs tls.ConnectionState, dry bool) (pullResp, int, error) {
	d.lock()
	id, kind, err := peerIdentity(cs, d.cl.Revoked)
	requester, ok := d.clusterNodeLocked(id)
	switch {
	case err != nil:
		d.unlock()
		return pullResp{}, http.StatusForbidden, err
	case kind != "node" || !ok || requester.Kind != "node" || id == d.node.ID:
		d.unlock()
		return pullResp{}, http.StatusForbidden, errors.New("Nur für Server des Verbunds")
	case d.p.Role != ha.Active || d.unsaved:
		d.unlock()
		return pullResp{}, http.StatusConflict, errors.New("Nicht der aktive Server")
	}
	self, _ := d.clusterNodeLocked(d.node.ID)
	cl := d.cl
	d.unlock()

	c, err := discoverCluster(ctx, d.sys)
	if err != nil {
		return pullResp{}, http.StatusInternalServerError, err
	}
	pg := &pgCtl{r: d.sys, c: c}
	resp := pullResp{PGMajor: c.Version}
	size, err := pg.query(ctx, "SELECT COALESCE(sum(pg_database_size(datname)), 0) FROM pg_database")
	if err != nil {
		return pullResp{}, http.StatusInternalServerError, err
	}
	resp.DBBytes, _ = strconv.ParseInt(size, 10, 64)
	if dry {
		return resp, http.StatusOK, nil
	}
	pass, err := d.replicaPass()
	if err != nil {
		return pullResp{}, http.StatusInternalServerError, err
	}
	pa := d.currentPGActor()
	if pa == nil {
		pa = newPGActor(pg, newServiceCtl(d.sys), d.ev, func() pgEnv { return buildPGEnv(self, requester, cl, pass, c, true) })
	}
	if err := pa.ensurePrimary(ctx, d.st.etc); err != nil {
		return pullResp{}, http.StatusInternalServerError, err
	}
	arch, err := configArchive(pdhRepo(), pdhEtc())
	if err != nil {
		return pullResp{}, http.StatusInternalServerError, err
	}
	resp.ReplicaPass, resp.Archive = pass, arch
	d.lock()
	d.selectActorLocked(ctx) // ab jetzt echte Aktionen auf der aktiven Seite
	d.unlock()
	d.event("info", "Datenbank-Spiegel für "+id+" eingerichtet")
	return resp, http.StatusOK, nil
}

func (d *daemon) currentPGActor() *pgActor {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pgActorLocked()
}

// replicaPass liest /etc/pdh/ha/replica.pass oder legt es an (0600).
func (d *daemon) replicaPass() (string, error) {
	p := d.st.etcPath("replica.pass")
	if b, err := os.ReadFile(p); err == nil {
		return strings.TrimSpace(string(b)), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	pass, err := newReplicaPass()
	if err != nil {
		return "", err
	}
	return pass, writeFileAtomic(p, []byte(pass+"\n"), 0o600)
}
