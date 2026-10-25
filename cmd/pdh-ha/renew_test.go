package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"

	"pdh/internal/ha"
)

// Erneuerung: wie beim Beitritt nur ECDSA P-256.
func TestSignLockedRequiresP256(t *testing.T) {
	st := store{etc: t.TempDir(), state: t.TempDir()}
	if err := setupFiles(st, setupOpts{ID: "a", Addrs: []string{"127.0.0.1:7480"}}); err != nil {
		t.Fatal(err)
	}
	cl, _ := st.loadCluster()
	cl.Nodes = append(cl.Nodes, clusterNode{ID: "b", Kind: "node", Addrs: []string{"127.0.0.2:7480"}})
	cl.Version++
	if err := st.saveCluster(cl); err != nil {
		t.Fatal(err)
	}
	if err := st.saveState(ha.Persisted{Role: ha.Active, Term: 1}); err != nil {
		t.Fatal(err)
	}
	d := loadFakeDaemon(t, st, newFakeNet())
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "b"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	d.lock()
	_, err = d.signLocked(csr, "b", "node")
	d.unlock()
	if err == nil || err.Error() != "Signieranforderung muss ECDSA P-256 und gültig signiert sein" {
		t.Fatalf("P-384 angenommen: %v", err)
	}
	_, good, _ := newKeyCSR("b", "node")
	d.lock()
	_, err = d.signLocked(good, "b", "node")
	d.unlock()
	if err != nil {
		t.Fatalf("P-256: %v", err)
	}
}
