package main

// Prüfung nach dem Neuladen: Eine neue nginx-Seite darf keinem anderen Namen ein anderes
// Zertifikat bescheren (Vorfall 21.11.2026: Seite mit „listen [::]:443“, die von Hand
// angelegte PDH-Seite nur IPv4 – der Tunnel bekam über ::1 das falsche Zertifikat).
// Vor dem Schreiben wird für jeden anderen Namen gemerkt, welches Zertifikat über
// 127.0.0.1 und ::1 kommt; danach wird verglichen.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// certProbeMax: höchstens so viele Namen werden geprüft (je Name zwei Proben gleichzeitig).
const certProbeMax = 32

// certProbe: Fingerabdruck (SHA-256, hex) des ersten Zertifikats, das nginx unter addr für
// name (SNI) zeigt. Variable, damit Tests sie ersetzen können.
var certProbe = func(ctx context.Context, addr, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	d := tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{ServerName: name, InsecureSkipVerify: true}} //nolint:gosec // nur Vergleich der Fingerabdrücke
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", err
	}
	defer c.Close()
	tc, ok := c.(*tls.Conn)
	if !ok {
		return "", errors.New("keine TLS-Verbindung")
	}
	certs := tc.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("kein Zertifikat")
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// certOtherNames: Namen aller anderen Seiten, die auf Port 443 hören (ohne skipEntry).
// Platzhalter (_), Muster (*, ~) und doppelte fallen weg; höchstens certProbeMax.
func (a *updateAgent) certOtherNames(skipEntry string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range a.certSiteFiles() {
		if f.entry == skipEntry || !(certListen443Re.MatchString(f.text) || certListen443V6Re.MatchString(f.text)) {
			continue
		}
		for _, m := range certServerNameR.FindAllStringSubmatch(f.text, -1) {
			for _, n := range strings.Fields(m[1]) {
				n = strings.TrimSuffix(strings.ToLower(n), ".")
				if n == "" || n == "_" || strings.ContainsAny(n, "*~") || seen[n] {
					continue
				}
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	if len(out) > certProbeMax {
		out = out[:certProbeMax]
	}
	return out
}

// certProbeAll: Fingerabdruck je „Adresse|Name“ (über 127.0.0.1, mit v6 auch ::1);
// keine Antwort = "". Alle Proben laufen gleichzeitig.
func certProbeAll(ctx context.Context, names []string, v6 bool) map[string]string {
	addrs := []string{"127.0.0.1:443"}
	if v6 {
		addrs = append(addrs, "[::1]:443")
	}
	out := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range names {
		for _, ad := range addrs {
			wg.Add(1)
			go func(ad, n string) {
				defer wg.Done()
				fp, err := certProbe(ctx, ad, n)
				if err != nil {
					fp = ""
				}
				mu.Lock()
				out[ad+"|"+n] = fp
				mu.Unlock()
			}(ad, n)
		}
	}
	wg.Wait()
	return out
}

// certDisturbed: erster Schlüssel (sortiert), bei dem die neue Seite einem anderen Namen
// schadet. own sind die Fingerabdrücke des eigenen Zertifikats (bisher und neu):
//   - Antwortete der Name vorher mit seinem eigenen Zertifikat und jetzt mit einem anderen
//     oder gar nicht → Störung.
//   - Kam vorher schon unser Zertifikat (der Name war fälschlich bei uns, Vorfall 202), ist
//     jede Änderung die Reparatur → keine Störung.
//   - Antwortete vorher niemand, jetzt aber unser Zertifikat, während derselbe Name über
//     127.0.0.1 ein eigenes hat → Störung (so lief der Vorfall über ::1).
func certDisturbed(before, after map[string]string, own map[string]bool) (string, bool) {
	keys := make([]string, 0, len(before))
	for k := range before {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b, a := before[k], after[k]
		switch {
		case b != "" && own[b]:
			continue
		case b != "" && a != b:
			return k, true
		case b == "" && a != "" && own[a]:
			_, name := certProbeKey(k)
			if v4 := before["127.0.0.1:443|"+name]; v4 != "" && !own[v4] {
				return k, true
			}
		}
	}
	return "", false
}

// certSettle: Abstand der Wiederholungen nach dem Neuladen („systemctl reload nginx“ kehrt
// zurück, bevor die alten Arbeitsprozesse fort sind). Variable für Tests.
var certSettle = 500 * time.Millisecond

// certProbeSettled: messen, bis zwei Läufe gleich sind (höchstens vier Läufe).
func certProbeSettled(ctx context.Context, names []string, v6 bool) map[string]string {
	prev := certProbeAll(ctx, names, v6)
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			return prev
		case <-time.After(certSettle):
		}
		cur := certProbeAll(ctx, names, v6)
		if certSameProbe(prev, cur) {
			return cur
		}
		prev = cur
	}
	return prev
}

func certSameProbe(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// certChainFP: Fingerabdruck des ersten Zertifikats einer PEM-Kette ("" = nicht lesbar).
func certChainFP(chain []byte) string {
	blk, _ := pem.Decode(chain)
	if blk == nil || blk.Type != "CERTIFICATE" {
		return ""
	}
	sum := sha256.Sum256(blk.Bytes)
	return hex.EncodeToString(sum[:])
}

// certProbeKey: „Adresse|Name“ zerlegen.
func certProbeKey(k string) (addr, name string) {
	addr, name, _ = strings.Cut(k, "|")
	return addr, name
}
