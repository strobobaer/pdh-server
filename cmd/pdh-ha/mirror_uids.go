package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"pdh/internal/ha"
)

// Benutzerkennungen im Verbund (I3/R70): rsync überträgt Besitzer als Nummer
// (--numeric-ids). Haben pdh oder www-data auf den Servern verschiedene
// Nummern, gehörten die gespiegelten Dateien am Partner einem fremden Konto –
// erst nach dem Tausch fiele das auf. Der Herzschlag trägt deshalb die eigenen
// Nummern; bei Abweichung hält der aktive Server die betroffenen Quellen an.

// mirrorUIDUsers: geprüfte Benutzer und die Quellen, deren Dateien ihnen gehören.
var mirrorUIDUsers = map[string][]string{
	"pdh":      {"uploads", "chat_files", "etc-pdh"},
	"www-data": {"nextcloud-data", "nextcloud-app"},
}

// uidsFromPasswd liest die Nummern der geprüften Benutzer aus dem Inhalt von
// /etc/passwd, z. B. "pdh=998,www-data=33" (fehlende Benutzer entfallen).
func uidsFromPasswd(passwd string) string {
	var out []string
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) < 3 {
			continue
		}
		if _, ok := mirrorUIDUsers[f[0]]; ok && f[2] != "" {
			out = append(out, f[0]+"="+f[2])
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// localUIDs: eigene Nummern von pdh und www-data ("" = keiner da oder unlesbar).
func localUIDs() string {
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	return uidsFromPasswd(string(b))
}

func parseUIDs(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k != "" && v != "" {
			m[k] = v
		}
	}
	return m
}

// uidMismatch vergleicht die eigenen Nummern mit denen des Partners und liefert
// je abweichendem Benutzer den Hinweis. Kennt eine Seite den Benutzer nicht
// (oder sendet der Partner das Feld noch nicht), gilt er als gleich.
func uidMismatch(own, partner string) map[string]string {
	if own == "" || partner == "" {
		return nil
	}
	o, p := parseUIDs(own), parseUIDs(partner)
	var out map[string]string
	for u := range mirrorUIDUsers {
		if o[u] != "" && p[u] != "" && o[u] != p[u] {
			if out == nil {
				out = map[string]string{}
			}
			out[u] = fmt.Sprintf("Benutzerkennung %s: hier %s, Partner %s – vor dem Spiegeln angleichen", u, o[u], p[u])
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

// uidHolds: angehaltene Quellen (Name -> Hinweis). Meldet eine neue Abweichung
// einmal als Alarm; ist sie behoben, darf eine spätere wieder melden.
func (w *mirrorWorker) uidHolds() map[string]string {
	if w.d.uids == nil {
		return nil
	}
	mm := uidMismatch(w.d.uids())
	text := uidWarnText(mm)
	w.mu.Lock()
	report := text != "" && text != w.uidAlarm
	w.uidAlarm = text
	w.mu.Unlock()
	if report {
		w.d.ev.add(ha.Event{Kind: "alarm", Text: "Spiegelung angehalten: " + text})
	}
	holds := map[string]string{}
	for u, t := range mm {
		for _, l := range mirrorUIDUsers[u] {
			holds[l] = t
		}
	}
	return holds
}
