package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"pdh/internal/ha"
)

// Einrichten (erster Knoten mit eigener CA) und Beitreten (weitere Knoten,
// Zeuge). Beides schreibt nur Dateien; ein laufender Daemon übernimmt sie.

var errConfigured = errors.New("Ausfallschutz ist bereits eingerichtet")

type setupOpts struct {
	ID    string   // leer = aus dem Hostnamen
	Addrs []string // leer = Adressen der Schnittstellen
}

type joinOpts struct {
	ID    string
	Kind  string // "node" | "witness"
	Addrs []string
}

func configured(st store) bool {
	_, err := os.Stat(st.etcPath("node.json"))
	return err == nil
}

func resolveID(id string) (string, error) {
	if id == "" {
		h, _ := os.Hostname()
		id = idFromHostname(h)
	}
	if !validID(id) {
		return "", fmt.Errorf("Ungültige Kennung %q", id)
	}
	return id, nil
}

func resolveAddrs(a []string) ([]string, error) {
	if len(a) == 0 {
		a = localAddrs()
	}
	return normAddrs(a)
}

// setupFiles richtet diesen Server als ersten Knoten ein: CA, Zertifikat,
// Cluster mit einem Knoten, Amtszeit 1, aktiv, Wartung an. node.json zuletzt –
// ihr Vorhandensein bedeutet „eingerichtet“.
func setupFiles(st store, o setupOpts) error {
	if configured(st) {
		return errConfigured
	}
	id, err := resolveID(o.ID)
	if err != nil {
		return err
	}
	addrs, err := resolveAddrs(o.Addrs)
	if err != nil {
		return err
	}
	rb := make([]byte, 4)
	if _, err := rand.Read(rb); err != nil {
		return err
	}
	clusterID := hex.EncodeToString(rb)
	caCert, caKey, err := newCA(clusterID)
	if err != nil {
		return err
	}
	key, csr, err := newKeyCSR(id, "node")
	if err != nil {
		return err
	}
	cert, err := signCSR(caCert, caKey, csr, "node", joinCertDays)
	if err != nil {
		return err
	}
	c, err := parseCertPEM(cert)
	if err != nil {
		return err
	}
	cl := clusterConfig{
		ClusterID: clusterID, Version: 1, Settings: ha.DefaultSettings(),
		Nodes: []clusterNode{{ID: id, Kind: "node", Addrs: addrs, Serial: serialHex(c)}},
	}
	steps := []func() error{
		func() error { return st.writeEtc("ca.crt", caCert, 0o644) },
		func() error { return st.writeEtc("ca.key", caKey, 0o600) },
		func() error { return st.writeEtc("node.crt", cert, 0o644) },
		func() error { return st.writeEtc("node.key", key, 0o600) },
		func() error { return st.saveCluster(cl) },
		func() error {
			return st.saveState(ha.Persisted{Term: 1, Role: ha.Active, Maint: true, MaintSeq: 1})
		},
		func() error { return st.saveNode(nodeConfig{ID: id, Kind: "node", Addrs: addrs, CAHolder: true}) },
	}
	for _, f := range steps {
		if err := f(); err != nil {
			return err
		}
	}
	return nil
}

// joinFiles tritt mit einem Beitrittscode bei: Schlüssel lokal erzeugen, nur
// die Signieranforderung verlässt den Rechner.
func joinFiles(ctx context.Context, st store, codeText string, o joinOpts) (nodeConfig, error) {
	if configured(st) {
		return nodeConfig{}, errConfigured
	}
	code, err := parseJoinCode(codeText)
	if err != nil {
		return nodeConfig{}, err
	}
	if o.Kind == "" {
		o.Kind = "node"
	}
	if code.Role != o.Kind {
		if code.Role == "witness" {
			return nodeConfig{}, errors.New("Der Beitrittscode gilt für einen Zeugen – mit --witness aufrufen")
		}
		return nodeConfig{}, errors.New("Der Beitrittscode gilt für einen Server – ohne --witness aufrufen")
	}
	id, err := resolveID(o.ID)
	if err != nil {
		return nodeConfig{}, err
	}
	addrs, err := resolveAddrs(o.Addrs)
	if err != nil {
		return nodeConfig{}, err
	}
	key, csr, err := newKeyCSR(id, o.Kind)
	if err != nil {
		return nodeConfig{}, err
	}
	resp, err := joinRemote(ctx, code, joinRequest{ID: id, CSR: string(csr), Addrs: addrs})
	if err != nil {
		return nodeConfig{}, err
	}
	role := ha.Passive
	if o.Kind == "witness" {
		role = ha.Witness
	}
	n := nodeConfig{ID: id, Kind: o.Kind, Addrs: addrs}
	steps := []func() error{
		func() error { return st.writeEtc("ca.crt", []byte(resp.CA), 0o644) },
		func() error { return st.writeEtc("node.crt", []byte(resp.Cert), 0o644) },
		func() error { return st.writeEtc("node.key", key, 0o600) },
		func() error { return st.saveCluster(resp.Cluster) },
		func() error { return st.saveState(ha.Persisted{Role: role}) },
		func() error { return st.saveNode(n) },
	}
	for _, f := range steps {
		if err := f(); err != nil {
			return nodeConfig{}, err
		}
	}
	return n, nil
}

func parseCertPEM(p []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(p)
	if b == nil {
		return nil, errors.New("Zertifikat ist unlesbar")
	}
	return x509.ParseCertificate(b.Bytes)
}
