package mqttbroker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func selfSigned(t *testing.T, cn string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func peerCN(t *testing.T, port int) (string, error) {
	t.Helper()
	d := &net.Dialer{Timeout: 3 * time.Second}
	c, err := tls.DialWithDialer(d, "tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].Subject.CommonName, nil
}

// Der TLS-Port läuft zusätzlich; ein getauschtes Zertifikat gilt für den nächsten
// Handshake, ohne Zertifikat scheitert nur der Handshake.
func TestTLSPortUsesGetCertificate(t *testing.T) {
	plain, tlsPort := freePort(t), freePort(t)
	var cur atomic.Pointer[tls.Certificate]
	m := NewManager()
	err := m.Start("c1", Config{ListenPort: plain, AllowAnonymous: true, TLSPort: tlsPort,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if c := cur.Load(); c != nil {
				return c, nil
			}
			return nil, errors.New("noch kein Zertifikat")
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()

	st, ok := m.Status("c1")
	if !ok || st.TLSAddr != fmt.Sprintf(":%d", tlsPort) {
		t.Fatalf("Status nennt den TLS-Port nicht: %+v", st)
	}
	if _, err := peerCN(t, tlsPort); err == nil {
		t.Fatal("Handshake ohne Zertifikat hätte scheitern müssen")
	}
	// der Port ohne TLS läuft trotzdem
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", plain), 3*time.Second)
	if err != nil {
		t.Fatalf("Listen-Port nicht erreichbar: %v", err)
	}
	c.Close()

	cur.Store(selfSigned(t, "alt.example"))
	if cn, err := peerCN(t, tlsPort); err != nil || cn != "alt.example" {
		t.Fatalf("erstes Zertifikat: %q %v", cn, err)
	}
	cur.Store(selfSigned(t, "neu.example"))
	if cn, err := peerCN(t, tlsPort); err != nil || cn != "neu.example" {
		t.Fatalf("erneuertes Zertifikat: %q %v", cn, err)
	}
}

func TestTLSPortOffWithoutGetter(t *testing.T) {
	plain, tlsPort := freePort(t), freePort(t)
	m := NewManager()
	if err := m.Start("c1", Config{ListenPort: plain, AllowAnonymous: true, TLSPort: tlsPort}); err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	if st, _ := m.Status("c1"); st.TLSAddr != "" {
		t.Fatalf("TLS-Port ohne Zertifikatsquelle gestartet: %q", st.TLSAddr)
	}
}

func TestTLSPortCollision(t *testing.T) {
	plain := freePort(t)
	m := NewManager()
	err := m.Start("c1", Config{ListenPort: plain, AllowAnonymous: true, TLSPort: plain,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return nil, errors.New("x") }})
	if err == nil {
		m.StopAll()
		t.Fatal("gleicher Port für Listen und TLS hätte abgelehnt werden müssen")
	}
	if m.IsRunning("c1") {
		t.Fatal("Broker gilt trotz Fehler als laufend")
	}
}
