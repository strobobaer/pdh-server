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
// Eine Angabe in PDH_LAST_PRODUCTION, die keine IP-Adresse ist (Tippfehler),
// stoppt die Lastprüfung, statt die Sperre still abzuschalten.
func forbidden(named map[string]string) error {
	ip, ok := productionIP()
	var re *regexp.Regexp
	var prod net.IP
	if ok {
		if prod = net.ParseIP(ip); prod == nil {
			return fmt.Errorf("PDH_LAST_PRODUCTION=%q ist keine IP-Adresse (erwartet z. B. 192.0.2.202) – die Lastprüfung startet nicht", ip)
		}
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
		if re.MatchString(v) || targetReaches(name, v, prod) {
			return fmt.Errorf("%s zeigt auf den Produktivserver %s – die Lastprüfung läuft nie gegen die Produktion", name, ip)
		}
	}
	return nil
}

// targetHosts: Rechner einer Angabe (Adresse oder Name); ok=false, wenn sie nicht lesbar ist.
func targetHosts(name, v string) ([]string, bool) {
	switch name {
	case "-base":
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return nil, false
		}
		return []string{u.Hostname()}, true
	case "-dsn":
		cfg, err := pgconn.ParseConfig(v)
		if err != nil {
			return nil, false
		}
		hosts := []string{cfg.Host}
		for _, f := range cfg.Fallbacks {
			hosts = append(hosts, f.Host)
		}
		return hosts, true
	case "-ssh":
		return []string{v[strings.LastIndex(v, "@")+1:]}, true
	}
	return nil, false
}

// targetReaches: führt ein Rechner der Angabe (auch über seinen Namen) zur Adresse prod?
func targetReaches(name, v string, prod net.IP) bool {
	hosts, _ := targetHosts(name, v)
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			if ip.Equal(prod) {
				return true
			}
			continue
		}
		addrs, _ := net.LookupIP(h)
		for _, a := range addrs {
			if a.Equal(prod) {
				return true
			}
		}
	}
	return false
}

// targetIsLocal: zeigt die Angabe nur auf diesen Rechner? Nicht lesbare Angaben gelten als fremd.
func targetIsLocal(name, v string) bool {
	hosts, ok := targetHosts(name, v)
	if !ok || len(hosts) == 0 {
		return false
	}
	for _, h := range hosts {
		if h != "localhost" {
			if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
				return false
			}
		}
	}
	return true
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
