package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// Erneuerung des eigenen Zertifikats ab 30 Tagen Restlaufzeit: der CA-Knoten
// signiert selbst, alle anderen fragen per renew_req. Der neue Schlüssel
// entsteht lokal, nur die Signieranforderung geht hinaus.

func (d *daemon) checkRenew(now time.Time) {
	d.lock()
	pl := plan{net: d.net}
	d.checkRenewLocked(&pl, now)
	d.unlock()
	d.execute(context.Background(), pl)
}

func (d *daemon) checkRenewLocked(pl *plan, now time.Time) {
	if d.certNotAfter.IsZero() || d.certNotAfter.Sub(now) > renewBefore || d.removed {
		return
	}
	if !d.renewAt.IsZero() && now.Sub(d.renewAt) < renewRetry {
		return
	}
	d.renewAt = now
	key, csr, err := newKeyCSR(d.node.ID, d.node.Kind)
	if err != nil {
		pl.event("warn", "Zertifikat-Erneuerung: "+err.Error())
		return
	}
	if d.node.CAHolder {
		cert, err := d.signLocked(csr, d.node.ID, d.node.Kind)
		if err == nil {
			err = d.installCertLocked(pl, cert, key)
		}
		if err != nil {
			pl.event("warn", "Zertifikat-Erneuerung: "+err.Error())
		}
		return
	}
	d.renewKey = key
	for _, n := range d.cl.Nodes {
		if n.Kind == "node" && n.ID != d.node.ID {
			pl.send(n.ID, message{T: "renew_req", ID: d.node.ID, CSR: string(csr), Seq: d.nextSeq()})
		}
	}
}

// signLocked signiert eine Anforderung mit der CA und trägt die neue
// Seriennummer in die Knotenliste ein.
func (d *daemon) signLocked(csrPEM []byte, id, kind string) ([]byte, error) {
	rb, _ := pem.Decode(csrPEM)
	if rb == nil {
		return nil, errors.New("Signieranforderung ist unlesbar")
	}
	csr, err := x509.ParseCertificateRequest(rb.Bytes)
	if err != nil || csr.Subject.CommonName != id {
		return nil, errors.New("Signieranforderung passt nicht zur Kennung")
	}
	caCert, err1 := os.ReadFile(d.st.etcPath("ca.crt"))
	caKey, err2 := os.ReadFile(d.st.etcPath("ca.key"))
	if err := errors.Join(err1, err2); err != nil {
		return nil, err
	}
	cert, err := signCSR(caCert, caKey, csrPEM, kind, joinCertDays)
	if err != nil {
		return nil, err
	}
	c, err := parseCertPEM(cert)
	if err != nil {
		return nil, err
	}
	cl := d.cl
	cl.Nodes = append([]clusterNode(nil), d.cl.Nodes...)
	for i := range cl.Nodes {
		if cl.Nodes[i].ID == id {
			cl.Nodes[i].Serial = serialHex(c)
		}
	}
	cl.Version++
	if err := d.st.saveCluster(cl); err != nil {
		return nil, err
	}
	d.cl = cl
	d.net.setCluster(cl)
	return cert, nil
}

func (d *daemon) renewReqLocked(pl *plan, from, kind string, m message) {
	cert, err := d.signLocked([]byte(m.CSR), from, kind)
	if err != nil {
		pl.event("warn", fmt.Sprintf("Zertifikat für %s nicht erneuert: %v", from, err))
		return
	}
	cl := d.cl
	pl.send(from, message{T: "renew_resp", ID: d.node.ID, Cert: string(cert), Seq: d.nextSeq()})
	pl.send("", message{T: "cluster", ID: d.node.ID, Seq: d.nextSeq(), Cluster: &cl})
	pl.event("info", fmt.Sprintf("Zertifikat für %s erneuert", from))
}

func (d *daemon) renewRespLocked(pl *plan, m message) {
	if d.renewKey == nil || m.Cert == "" {
		return
	}
	if err := d.installCertLocked(pl, []byte(m.Cert), d.renewKey); err != nil {
		pl.event("warn", "Erneuertes Zertifikat abgelehnt: "+err.Error())
		return
	}
	d.renewKey = nil
}

// installCertLocked prüft das neue Zertifikat und speichert es. Schlüssel und
// Zertifikat werden erst als *.new geschrieben; bricht es dazwischen ab, findet
// loadCert das neue Zertifikat.
func (d *daemon) installCertLocked(pl *plan, certPEM, keyPEM []byte) error {
	caPEM, err := os.ReadFile(d.st.etcPath("ca.crt"))
	if err != nil {
		return err
	}
	c, err := parseCertPEM(certPEM)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	if _, err := c.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return errors.New("nicht von der Cluster-CA")
	}
	if c.Subject.CommonName != d.node.ID {
		return errors.New("falsche Kennung")
	}
	tc, err := loadTLSCert(certPEM, keyPEM, caPEM)
	if err != nil {
		return err
	}
	for _, f := range []func() error{
		func() error { return d.st.writeEtc("node.crt.new", certPEM, 0o644) },
		func() error { return d.st.writeEtc("node.key.new", keyPEM, 0o600) },
		func() error { return os.Rename(d.st.etcPath("node.key.new"), d.st.etcPath("node.key")) },
		func() error { return os.Rename(d.st.etcPath("node.crt.new"), d.st.etcPath("node.crt")) },
	} {
		if err := f(); err != nil {
			return err
		}
	}
	d.net.setCert(tc)
	d.certNotAfter = c.NotAfter
	pl.event("info", "Zertifikat erneuert, gültig bis "+c.NotAfter.Local().Format("02.01.2006"))
	return nil
}

// loadCert lädt node.crt/node.key; nach unterbrochener Erneuerung auch node.crt.new.
func (d *daemon) loadCert() (tls.Certificate, time.Time, error) {
	caPEM, _ := os.ReadFile(d.st.etcPath("ca.crt"))
	keyPEM, err := os.ReadFile(d.st.etcPath("node.key"))
	if err != nil {
		return tls.Certificate{}, time.Time{}, err
	}
	try := func(name string) (tls.Certificate, time.Time, error) {
		certPEM, err := os.ReadFile(d.st.etcPath(name))
		if err != nil {
			return tls.Certificate{}, time.Time{}, err
		}
		c, err := loadTLSCert(certPEM, keyPEM, caPEM)
		if err != nil {
			return c, time.Time{}, err
		}
		x, err := parseCertPEM(certPEM)
		if err != nil {
			return c, time.Time{}, err
		}
		return c, x.NotAfter, nil
	}
	c, na, err := try("node.crt")
	if err != nil {
		if c2, na2, err2 := try("node.crt.new"); err2 == nil {
			os.Rename(d.st.etcPath("node.crt.new"), d.st.etcPath("node.crt"))
			return c2, na2, nil
		}
	}
	return c, na, err
}
