package main

import (
	"os"
	"sort"
	"strings"

	"pdh/internal/ha"
)

// Benutzerkennungen im Verbund (I3/R70): rsync überträgt Besitzer und Gruppe als
// Nummer (--numeric-ids). Haben pdh oder www-data auf den Servern verschiedene
// Nummern, gehörten die gespiegelten Dateien am Partner einem fremden Konto –
// erst nach dem Tausch fiele das auf. Der Herzschlag trägt deshalb die eigenen
// Nummern (auch bei ausgeschalteter Spiegelung, damit sie beim Einschalten schon
// da sind); bei Abweichung hält der aktive Server die betroffenen Quellen an.

// mirrorUIDUsers: geprüfte Benutzer und die Quellen, deren Dateien ihnen gehören.
var mirrorUIDUsers = map[string][]string{
	"pdh":      {"uploads", "chat_files", "etc-pdh"},
	"www-data": {"nextcloud-data", "nextcloud-app"},
}

// uidHoldError: Quelle wegen abweichender Kennung angehalten. Die Karte nennt es
// einmal (UIDWarn), nicht zusätzlich als Fehler des Laufs oder der Sofort-Übertragung.
type uidHoldError string

func (e uidHoldError) Error() string { return string(e) }

// uidsFromFiles liest die Nummern der geprüften Benutzer aus dem Inhalt von
// /etc/passwd (Benutzernummer) und /etc/group (Nummer der gleichnamigen Gruppe),
// z. B. "pdh=998:998,www-data=33:33". Fehlende Benutzer entfallen, eine fehlende
// Gruppe bleibt leer ("pdh=998:").
func uidsFromFiles(passwd, group string) string {
	gids := map[string]string{}
	for _, line := range strings.Split(group, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) >= 3 {
			if _, ok := mirrorUIDUsers[f[0]]; ok {
				gids[f[0]] = f[2]
			}
		}
	}
	var out []string
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) < 3 {
			continue
		}
		if _, ok := mirrorUIDUsers[f[0]]; ok && f[2] != "" {
			out = append(out, f[0]+"="+f[2]+":"+gids[f[0]])
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// localUIDs: eigene Nummern von pdh und www-data ("" = keiner da oder unlesbar).
func localUIDs() string {
	p, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	g, _ := os.ReadFile("/etc/group")
	return uidsFromFiles(string(p), string(g))
}

// parseUIDs: Benutzer -> [Benutzernummer, Gruppennummer] ("" = unbekannt).
func parseUIDs(s string) map[string][2]string {
	m := map[string][2]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k != "" && v != "" {
			u, g, _ := strings.Cut(v, ":")
			m[k] = [2]string{u, g}
		}
	}
	return m
}

// mirrorUIDUsersFor: Benutzer, deren Quellen bei diesen Einstellungen gespiegelt
// werden (www-data nur mit „Nextcloud mitnehmen“).
func mirrorUIDUsersFor(nextcloud bool) []string {
	if nextcloud {
		return []string{"pdh", "www-data"}
	}
	return []string{"pdh"}
}

// uidMismatch vergleicht die eigenen Nummern mit denen des Partners und liefert
// je abweichendem Benutzer den Hinweis. Kennt eine Seite eine Nummer nicht (oder
// sendet der Partner das Feld noch nicht), gilt sie als gleich.
func uidMismatch(own, partner string, nextcloud bool) map[string]string {
	if own == "" || partner == "" {
		return nil
	}
	o, p := parseUIDs(own), parseUIDs(partner)
	var out map[string]string
	for _, u := range mirrorUIDUsersFor(nextcloud) {
		var parts []string
		for i, what := range []string{"Benutzerkennung", "Gruppenkennung"} {
			if a, b := o[u][i], p[u][i]; a != "" && b != "" && a != b {
				parts = append(parts, what+" "+u+": hier "+a+", Partner "+b)
			}
		}
		if len(parts) > 0 {
			if out == nil {
				out = map[string]string{}
			}
			out[u] = strings.Join(parts, "; ") + " – vor dem Spiegeln angleichen"
		}
	}
	return out
}

// uidWarnText: alle Hinweise in fester Reihenfolge, durch „; “ getrennt.
func uidWarnText(mm map[string]string) string {
	users := make([]string, 0, len(mm))
	for u := range mm {
		users = append(users, u)
	}
	sort.Strings(users)
	texts := make([]string, 0, len(users))
	for _, u := range users {
		texts = append(texts, mm[u])
	}
	return strings.Join(texts, "; ")
}

// uidHold: Hinweis, wenn diese Quelle wegen abweichender Kennung nicht laufen darf
// ("" = darf). Liest den Stand frisch (je Quelle vor ihrem Lauf) und meldet eine
// neue Abweichung einmal als Alarm; ist sie behoben, darf eine spätere wieder melden.
func (w *mirrorWorker) uidHold(label string) string {
	if w.d.uids == nil {
		return ""
	}
	w.mu.Lock()
	nc := w.cfg.MirrorNextcloud
	w.mu.Unlock()
	own, partner := w.d.uids()
	mm := uidMismatch(own, partner, nc)
	text := uidWarnText(mm)
	w.mu.Lock()
	report := text != "" && text != w.uidAlarm
	w.uidAlarm = text
	w.mu.Unlock()
	if report {
		w.d.ev.add(ha.Event{Kind: "alarm", Text: "Spiegelung angehalten: " + text})
	}
	for u, t := range mm {
		for _, l := range mirrorUIDUsers[u] {
			if l == label {
				return t
			}
		}
	}
	return ""
}
