// Package acme holt Zertifikate von Let's Encrypt per DNS-01. Der Ablauf (Issue) kennt
// weder Cloudflare noch Let's Encrypt, sondern nur die Schnittstellen DNS und CA;
// die echte Anbindung steht in le.go bzw. im Cloudflare-Paket.
package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DNS legt den Prüf-Eintrag an, fragt seine Sichtbarkeit ab und räumt ihn wieder weg.
type DNS interface {
	Present(ctx context.Context, fqdn, value string) (id string, err error)
	CleanUp(ctx context.Context, id string) error
	Visible(ctx context.Context, fqdn, value string) (bool, error)
}

// Order ist ein offener Auftrag bei der Zertifizierungsstelle.
type Order interface {
	// Challenge liefert den Namen und den Wert des Prüf-Eintrags.
	Challenge() (fqdn, value string)
	// Accept meldet der Stelle, dass der Eintrag steht, und wartet auf ihre Prüfung.
	Accept(ctx context.Context) error
	// Finalize reicht die Zertifikatsanforderung ein und liefert die Kette (PEM).
	Finalize(ctx context.Context, csrDER []byte) (chainPEM []byte, err error)
}

// CA ist die Zertifizierungsstelle.
type CA interface {
	NewOrder(ctx context.Context, name string) (Order, error)
	Revoke(ctx context.Context, certPEM []byte) error
}

// Issued ist ein frisch ausgestelltes Zertifikat samt Schlüssel.
type Issued struct {
	ChainPEM, KeyPEM    []byte
	NotBefore, NotAfter time.Time
	Fingerprint         string // SHA-256 des Blatt-Zertifikats, hex
}

// RateLimited meldet, dass die Zertifizierungsstelle zu viele Anfragen sieht.
type RateLimited struct {
	Until time.Time
	Msg   string
}

func (e *RateLimited) Error() string {
	if e.Msg != "" {
		return "Zertifizierungsstelle begrenzt die Anfragen: " + e.Msg
	}
	return "Zertifizierungsstelle begrenzt die Anfragen"
}

const (
	// maxWarten ist die längste Zeit, die wir auf die Sichtbarkeit des Prüf-Eintrags warten.
	maxWarten = 2 * time.Minute
	// pollIntervall ist der Abstand der Sichtbarkeitsabfragen.
	pollIntervall = 5 * time.Second
)

// Issue holt ein Zertifikat für genau einen Namen. step meldet den Fortschritt
// ("auftrag", "dns", "warten", "pruefung", "abholen"); wait wartet die genannte Zeit
// (oder bricht mit ctx ab). Der Prüf-Eintrag wird immer wieder gelöscht, sobald er angelegt wurde.
// Der Schlüssel ist bei jedem Aufruf neu (ECDSA P-256).
func Issue(ctx context.Context, ca CA, dns DNS, name string, step func(string), wait func(context.Context, time.Duration) error) (*Issued, error) {
	if step == nil {
		step = func(string) {}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("Schlüssel erzeugen: %w", err)
	}

	step("auftrag")
	order, err := ca.NewOrder(ctx, name)
	if err != nil {
		return nil, err
	}

	step("dns")
	fqdn, value := order.Challenge()
	id, err := dns.Present(ctx, fqdn, value)
	if err != nil {
		return nil, err
	}
	defer func() {
		// Auch nach Abbruch aufräumen, aber nicht ewig.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = dns.CleanUp(cctx, id)
	}()

	step("warten")
	var gewartet time.Duration
	for {
		ok, err := dns.Visible(ctx, fqdn, value)
		if err != nil {
			return nil, err
		}
		if ok {
			break
		}
		if gewartet >= maxWarten {
			return nil, fmt.Errorf("Prüf-Eintrag %s nach %d Minuten noch nicht sichtbar", fqdn, int(maxWarten.Minutes()))
		}
		if err := wait(ctx, pollIntervall); err != nil {
			return nil, err
		}
		gewartet += pollIntervall
	}

	step("pruefung")
	if err := order.Accept(ctx); err != nil {
		return nil, err
	}

	step("abholen")
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{name}}, key)
	if err != nil {
		return nil, fmt.Errorf("Zertifikatsanforderung erzeugen: %w", err)
	}
	chain, err := order.Finalize(ctx, csr)
	if err != nil {
		return nil, err
	}

	blk, _ := pem.Decode(chain)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return nil, errors.New("Antwort der Zertifizierungsstelle enthält kein Zertifikat")
	}
	leaf, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("Zertifikat lesen: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("Schlüssel kodieren: %w", err)
	}
	sum := sha256.Sum256(leaf.Raw)
	return &Issued{
		ChainPEM:    chain,
		KeyPEM:      pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		NotBefore:   leaf.NotBefore,
		NotAfter:    leaf.NotAfter,
		Fingerprint: hex.EncodeToString(sum[:]),
	}, nil
}

// RenewAt ist der Zeitpunkt, ab dem erneuert wird: 30 Tage vor Ablauf, bei kurzer
// Laufzeit ein Drittel der Laufzeit vor Ablauf.
func RenewAt(notBefore, notAfter time.Time) time.Time {
	vor := notAfter.Sub(notBefore) / 3
	if vor > 30*24*time.Hour {
		vor = 30 * 24 * time.Hour
	}
	return notAfter.Add(-vor)
}

// NormalizeName bringt einen Rechnernamen in die Form für das Zertifikat (klein, ohne
// Schlusspunkt) und lehnt ab, was kein einzelner vollständiger Name ist (Platzhalter, Leerzeichen, nur ein Label).
func NormalizeName(s string) (string, error) {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if n == "" {
		return "", errors.New("Name fehlt")
	}
	if len(n) > 253 {
		return "", errors.New("Name ist zu lang")
	}
	labels := strings.Split(n, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("%q ist kein vollständiger Name (z. B. nc.firma.de)", s)
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", fmt.Errorf("%q ist kein gültiger Name", s)
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", fmt.Errorf("%q enthält ein unzulässiges Zeichen", s)
			}
		}
	}
	return n, nil
}
