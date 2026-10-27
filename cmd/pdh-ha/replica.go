package main

// Passive Seite: Knoten 2 als Standby des aktiven Knotens einrichten
// (pdh-ha replica-init), Hauptversion abfragen und sagen, ob pdh laufen soll.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pdh/internal/ha"
)

// pullFromActive fragt die Server des Verbunds der Reihe nach; der aktive
// antwortet, die übrigen mit 409. Mit mTLS über das eigene Knotenzertifikat.
func pullFromActive(ctx context.Context, st store, dry bool) (pullResp, clusterNode, error) {
	n, err := st.loadNode()
	if err != nil {
		return pullResp{}, clusterNode{}, fmt.Errorf("Nicht im Ausfallschutz eingerichtet: %w", err)
	}
	cl, err := st.loadCluster()
	if err != nil {
		return pullResp{}, clusterNode{}, err
	}
	caPEM, err := os.ReadFile(st.etcPath("ca.crt"))
	if err != nil {
		return pullResp{}, clusterNode{}, err
	}
	certPEM, err := os.ReadFile(st.etcPath("node.crt"))
	if err != nil {
		return pullResp{}, clusterNode{}, err
	}
	keyPEM, err := os.ReadFile(st.etcPath("node.key"))
	if err != nil {
		return pullResp{}, clusterNode{}, err
	}
	cert, err := loadTLSCert(certPEM, keyPEM, caPEM)
	if err != nil {
		return pullResp{}, clusterNode{}, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	body, _ := json.Marshal(map[string]bool{"dry": dry})
	var lastErr error = errors.New("Kein anderer Server im Verbund")
	for _, peer := range cl.Nodes {
		if peer.Kind != "node" || peer.ID == n.ID {
			continue
		}
		client := &http.Client{Timeout: 10 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: peer.ID,
			NextProtos: []string{alpnJoin}, MinVersion: tls.VersionTLS13,
		}}}
		for _, addr := range peer.Addrs {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/v1/config/pull", bytes.NewReader(body))
			if err != nil {
				return pullResp{}, clusterNode{}, err
			}
			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				var pr pullResp
				if err := json.Unmarshal(b, &pr); err != nil {
					return pullResp{}, clusterNode{}, err
				}
				return pr, peer, nil
			case http.StatusConflict:
				lastErr = fmt.Errorf("%s ist nicht der aktive Server", peer.ID)
			default:
				return pullResp{}, clusterNode{}, fmt.Errorf("%s: %s", peer.ID, strings.TrimSpace(string(b)))
			}
			break // dieser Server hat geantwortet, nächsten fragen
		}
	}
	return pullResp{}, clusterNode{}, lastErr
}

type replicaOpts struct {
	ConfirmHost string
	Repo, Etc   string
	Hostname    func() (string, error)
	Pull        func(ctx context.Context, dry bool) (pullResp, clusterNode, error)
	Wait        time.Duration // Frist bis „streaming“
}

// replicaInit macht diesen Server zum Standby des aktiven Knotens.
func replicaInit(ctx context.Context, st store, pa *pgActor, o replicaOpts) error {
	n, err := st.loadNode()
	if err != nil {
		return err
	}
	if n.CAHolder {
		return errors.New("Dies ist der erste Server (CA-Halter) – replica-init nur auf dem zweiten Server")
	}
	if p, err := st.loadState(); err == nil && p.Role == ha.Active {
		return errors.New("Dieser Server ist aktiv – replica-init nur auf dem passiven Server")
	}
	info, _, err := o.Pull(ctx, true)
	if err != nil {
		return err
	}
	if info.PGMajor != pa.pg.c.Version {
		return fmt.Errorf("Andere PostgreSQL-Hauptversion (hier %s, aktiv %s) – bitte postgresql-%s installieren", pa.pg.c.Version, info.PGMajor, info.PGMajor)
	}
	dbs, err := pa.pg.query(ctx, "SELECT datname || ' (' || pg_size_pretty(pg_database_size(datname)) || ')' FROM pg_database WHERE datname NOT IN ('postgres', 'template0', 'template1') ORDER BY 1")
	if err != nil {
		return fmt.Errorf("Lokale Datenbank nicht lesbar: %w", err)
	}
	if dbs = strings.TrimSpace(dbs); dbs != "" {
		host, _ := o.Hostname()
		if o.ConfirmHost == "" || o.ConfirmHost != host {
			return fmt.Errorf("Vorhandene Datenbanken: %s – zum Überschreiben --confirm-host %s angeben", strings.ReplaceAll(dbs, "\n", ", "), host)
		}
		dir := st.statePath("vorher")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		dump := filepath.ToSlash(filepath.Join(dir, pa.now().Format("20060102-150405")+".sql.gz"))
		if _, err := pa.pg.r.Run(ctx, []string{"bash", "-c", "set -o pipefail; runuser -u postgres -- pg_dumpall | gzip > " + shellQuote(dump)}, nil, nil); err != nil {
			return fmt.Errorf("Sicherung der vorhandenen Daten fehlgeschlagen – nichts überschrieben: %w", err)
		}
	}
	full, active, err := o.Pull(ctx, false)
	if err != nil {
		return err
	}
	if err := extractConfig(full.Archive, o.Repo, o.Etc); err != nil {
		return fmt.Errorf("Konfiguration nicht übernommen: %w", err)
	}
	e := pa.env()
	e.PartnerID, e.PartnerAddrs, e.ReplicaPass = active.ID, nil, full.ReplicaPass
	for _, a := range active.Addrs {
		e.PartnerAddrs = append(e.PartnerAddrs, hostOfAddr(a))
	}
	pa.env = func() pgEnv { return e }
	if err := pa.copyCerts(ctx, st.etc, e); err != nil {
		return err
	}
	if err := pa.writeAccess(ctx, e); err != nil {
		return err
	}
	if err := pa.svc.Stop(ctx); err != nil {
		return err
	}
	if err := pa.pg.ctl(ctx, "stop"); err != nil && pa.pg.status(ctx).State != "stopped" {
		return err
	}
	if err := pa.fullCopy(ctx, e); err != nil {
		return err
	}
	if err := pa.becomeStandby(ctx, e); err != nil {
		return err
	}
	if err := pa.pg.ctl(ctx, "start"); err != nil {
		return err
	}
	deadline := time.Now().Add(o.Wait)
	for {
		if s := pa.pg.status(ctx); s.State == "standby" && s.Streaming {
			break
		}
		if !time.Now().Before(deadline) {
			return errors.New("Standby läuft, empfängt aber noch nichts vom aktiven Server – Firewall (Port 5432) und Protokoll prüfen")
		}
		time.Sleep(pa.poll)
	}
	// erst jetzt: replica.pass schaltet den laufenden Dienst auf den
	// Datenbank-Ausführer, und der hielte eine laufende Neukopie für abgebrochen
	if err := st.writeEtc("replica.pass", []byte(full.ReplicaPass+"\n"), 0o600); err != nil {
		return err
	}
	return pa.svc.SetAutostart(ctx, false)
}

// shouldRunPDH: pdh darf laufen, wenn kein Verbund eingerichtet ist, Einzel
// oder dieser Knoten aktiv ist (für Update-Skripte).
func shouldRunPDH(st store) bool {
	if _, err := st.loadNode(); err != nil {
		return true
	}
	cl, err := st.loadCluster()
	if err != nil || cl.Mode() == ha.Single {
		return true
	}
	if _, err := os.Stat(st.etcPath("replica.pass")); err != nil {
		return true // ohne Datenbank-Spiegel wie bisher
	}
	p, err := st.loadState()
	return err == nil && p.Role == ha.Active
}
