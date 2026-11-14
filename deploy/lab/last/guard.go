package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Der produktive Server wird nie getestet: Jede Angabe, die auf diese Adresse
// zeigt, bricht das Programm ab.
const productionIP = "10.10.0.202"

var productionRe = regexp.MustCompile(`(^|[^0-9])10\.10\.0\.202([^0-9]|$)`)

// forbidden prüft Adressen (-base, -dsn, -ssh) auf den Produktivserver.
func forbidden(named map[string]string) error {
	for _, name := range []string{"-base", "-dsn", "-ssh"} {
		if v := named[name]; v != "" && productionRe.MatchString(v) {
			return fmt.Errorf("%s zeigt auf den Produktivserver %s – die Lastprüfung läuft nie gegen die Produktion", name, productionIP)
		}
	}
	return nil
}

// sshHostRe: nur Benutzer@Rechner, nichts, was ssh als Option lesen könnte.
var sshHostRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]*$`)

func checkSSHHost(h string) error {
	if !sshHostRe.MatchString(h) {
		return fmt.Errorf("-ssh: ungültige Angabe %q (erwartet benutzer@rechner)", h)
	}
	return nil
}

// describeDSN: Rechner:Port/Datenbank ohne Zugangsdaten, für Hinweise und Berichte.
func describeDSN(dsn string) string {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return "(DSN nicht lesbar)"
	}
	return fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
}

// checkBase: gültige http(s)-Adresse; ohne abschließenden Schrägstrich.
func checkBase(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("-base: ungültige Adresse %q (erwartet http://rechner:port)", base)
	}
	return strings.TrimRight(base, "/"), nil
}
