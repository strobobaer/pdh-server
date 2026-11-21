package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Der produktive Server wird nie getestet: Jede Angabe, die auf seine Adresse
// zeigt, bricht das Programm ab. Die Adresse steht in der Umgebungsvariablen
// PDH_LAST_PRODUCTION. Fehlt sie, ist nur dieser Rechner (localhost,
// 127.0.0.1, ::1) als Ziel erlaubt – die Sperre greift also immer.
func productionIP() (string, bool) {
	v := strings.TrimSpace(os.Getenv("PDH_LAST_PRODUCTION"))
	return v, v != ""
}

// forbidden prüft Adressen (-base, -dsn, -ssh) auf den Produktivserver.
func forbidden(named map[string]string) error {
	ip, ok := productionIP()
	var re *regexp.Regexp
	if ok {
		re = regexp.MustCompile(`(^|[^0-9])` + regexp.QuoteMeta(ip) + `([^0-9]|$)`)
	}
	for _, name := range []string{"-base", "-dsn", "-ssh"} {
		v := named[name]
		if v == "" {
			continue
		}
		if !ok {
			if !targetIsLocal(name, v) {
				return fmt.Errorf("%s zeigt nicht auf diesen Rechner – für andere Ziele zuerst PDH_LAST_PRODUCTION=<Adresse des Produktivservers> setzen, damit die Lastprüfung ihn sicher auslässt", name)
			}
			continue
		}
		if re.MatchString(v) {
			return fmt.Errorf("%s zeigt auf den Produktivserver %s – die Lastprüfung läuft nie gegen die Produktion", name, ip)
		}
	}
	return nil
}

// targetIsLocal: zeigt die Angabe nur auf diesen Rechner? Nicht lesbare Angaben gelten als fremd.
func targetIsLocal(name, v string) bool {
	var hosts []string
	switch name {
	case "-base":
		u, err := url.Parse(v)
		if err != nil {
			return false
		}
		hosts = []string{u.Hostname()}
	case "-dsn":
		cfg, err := pgconn.ParseConfig(v)
		if err != nil {
			return false
		}
		hosts = []string{cfg.Host}
		for _, f := range cfg.Fallbacks {
			hosts = append(hosts, f.Host)
		}
	case "-ssh":
		hosts = []string{v[strings.LastIndex(v, "@")+1:]}
	default:
		return false
	}
	for _, h := range hosts {
		if h != "localhost" {
			if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
				return false
			}
		}
	}
	return len(hosts) > 0
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
