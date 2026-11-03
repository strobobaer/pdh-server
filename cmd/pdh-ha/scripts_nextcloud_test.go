package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// scriptText liest ein Skript aus scripts/ mit LF-Zeilenenden.
func scriptText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../scripts/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// scriptFuncOf holt eine Funktion „name() {“ … „}“ aus einem Skript.
func scriptFuncOf(t *testing.T, file, name string) string {
	t.Helper()
	s := scriptText(t, file)
	i := strings.Index(s, "\n"+name+"() {\n")
	if i < 0 {
		t.Fatalf("%s nicht in %s gefunden", name, file)
	}
	s = s[i+1:]
	j := strings.Index(s, "\n}\n")
	if j < 0 {
		t.Fatalf("Ende von %s nicht gefunden", name)
	}
	return s[:j+3]
}

const ncScript = "install-nextcloud-nginx-postgres.sh"

func mustNotContain(t *testing.T, what, body string, bad ...string) {
	t.Helper()
	for _, b := range bad {
		if strings.Contains(body, b) {
			t.Errorf("%s darf %q nicht enthalten", what, b)
		}
	}
}

func mustContain(t *testing.T, what, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s muss %q enthalten", what, w)
		}
	}
}

// TestNextcloudScriptSyntax: das Nextcloud-Skript bleibt gültig.
func TestNextcloudScriptSyntax(t *testing.T) {
	bash := bashOrSkip(t)
	if out, err := exec.Command(bash, "-n", "../../scripts/"+ncScript).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

// TestNextcloudVerbundPassiv: am passiven Server nur Pakete, PHP und nginx – keine
// Datenbank, kein occ, PHP-FPM aus und ohne Autostart, Cron nur als Sicherung
// außerhalb von /etc/cron.d (R37).
func TestNextcloudVerbundPassiv(t *testing.T) {
	f := scriptFuncOf(t, ncScript, "verbund_passiv")
	mustNotContain(t, "verbund_passiv", f, "psql", "createdb", "occ", "enable postgresql", "restart postgresql",
		"systemctl enable", "restart \"${PHP_FPM_SERVICE}\"", "cat > /etc/cron.d/nextcloud")
	mustContain(t, "verbund_passiv", f, "disable --now \"${PHP_FPM_SERVICE}\"", "/var/lib/pdh-ha/nextcloud.cron",
		"mkdir -p \"$(dirname", "www-data:www-data", "verbund_pruefen passiv")
	if strings.Contains(scriptText(t, ncScript), "nextcloud.pdh-aus") {
		t.Error("nextcloud.pdh-aus kommt im Skript vor")
	}
}

// TestNextcloudVerbundAktivNeu: Anlegen wie bisher, aber Postgres bleibt unangetastet.
func TestNextcloudVerbundAktivNeu(t *testing.T) {
	s := scriptText(t, ncScript)
	mustContain(t, "Skript", s, "aktiv-neu", "maintenance:install", "createdb")
	// Die Postgres-Zeilen am Ende laufen nur ohne --verbund.
	i := strings.Index(s, "systemctl enable postgresql")
	if i < 0 {
		t.Fatal("systemctl enable postgresql fehlt (Normalmodus)")
	}
	before := s[:i]
	k := strings.LastIndex(before, "\nif ")
	if k < 0 || !strings.Contains(before[k:], `-z "${VERBUND}"`) {
		t.Errorf("enable/restart postgresql muss in einer Prüfung auf leeres VERBUND stehen:\n%s", before[k:])
	}
	// Das Postgres-Paket kommt im Verbund nicht mehr dazu.
	mustContain(t, "Paketliste", s, `-n "${VERBUND}" ]] && PG_PACKAGES=()`)
	// Ein Neustart von Postgres steht nirgends außerhalb dieser Prüfung.
	if strings.Count(s, "restart postgresql") != 1 {
		t.Errorf("restart postgresql muss genau einmal vorkommen, kommt %d-mal", strings.Count(s, "restart postgresql"))
	}
	mustNotContain(t, "Skript", s, "pg_ctlcluster")
}

// TestNextcloudVerbundAktivVorhanden: nur Prüfungen, nichts geschrieben.
func TestNextcloudVerbundAktivVorhanden(t *testing.T) {
	f := scriptFuncOf(t, ncScript, "verbund_vorhanden")
	mustContain(t, "verbund_vorhanden", f, "config/config.php", "exit 1", "verbund_pruefen aktiv", "verbund_weiter")
	mustNotContain(t, "verbund_vorhanden", f, "apt-get", "mkdir", "chown", "chmod", "sed -i", "cat >", "systemctl",
		"psql", "occ", "rm ", "tee")
}

// TestNextcloudOhneVerbundUnveraendert: ohne --verbund bleibt der Ablauf wie bisher.
func TestNextcloudOhneVerbundUnveraendert(t *testing.T) {
	s := scriptText(t, ncScript)
	mustContain(t, "Normalmodus", s,
		"PG_PACKAGES=(postgresql postgresql-contrib)",
		`[[ "${RESTORE}" == "1" ]] && PG_PACKAGES=()`,
		"maintenance:data-fingerprint", "maintenance:mode --off",
		"cat > /etc/cron.d/nextcloud <<CRON",
		"systemctl enable postgresql", "systemctl restart postgresql",
		`--restore) RESTORE="1"; shift ;;`,
		"already appears to be installed")
	// Das Ende des Normalmodus: restart nur ohne --restore.
	mustContain(t, "Postgres-Ende", s, `[[ "${RESTORE}" == "1" ]] || systemctl restart postgresql`)
	// Der Verbund-Zweig beendet das Skript, bevor der Normalablauf beginnt.
	mustContain(t, "Verteiler", s, "passiv) verbund_passiv; exit 0 ;;", "aktiv-vorhanden) verbund_vorhanden; exit 0 ;;")
	// Falsche Aufrufe werden abgelehnt (vor jeder Änderung am System).
	bash := bashOrSkip(t)
	for _, args := range [][]string{{"--verbund", "quatsch"}, {"--verbund", "passiv", "--restore"}} {
		out, err := exec.Command(bash, append([]string{"../../scripts/" + ncScript}, args...)...).CombinedOutput()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() == 0 {
			t.Errorf("%v: erwartet Fehlerausgang, bekam %v\n%s", args, err, out)
		}
	}
}

// TestNextcloudVerbundPruefungen: Rolle, Kennungen und pg_hba werden geprüft.
func TestNextcloudVerbundPruefungen(t *testing.T) {
	f := scriptFuncOf(t, ncScript, "verbund_pruefen")
	mustContain(t, "verbund_pruefen", f, "pdh-ha status --json", "--uid-www-data", "--uid-pdh", "pg_hba.conf", "exit 1")
	mustNotContain(t, "verbund_pruefen", f, "psql", "restart", "systemctl enable")
	w := scriptFuncOf(t, ncScript, "verbund_weiter")
	mustContain(t, "verbund_weiter", w, "--uid-www-data", "--uid-pdh", "--verbund passiv", "--data-dir")
}

// TestJoinPackagesRsync: rsync steht bei den Paketen für Server, nicht für den Zeugen.
func TestJoinPackagesRsync(t *testing.T) {
	j := scriptText(t, "pdh-ha-join.sh")
	i := strings.Index(j, "for p in git iputils-ping")
	if i < 0 {
		t.Fatal("Paketliste in pdh-ha-join.sh nicht gefunden")
	}
	mustContain(t, "pdh-ha-join.sh", j, "rsync", "(( WITNESS )) || ", "PDH_HA_WITNESS=1")
	if strings.Contains(j[i:i+120], "rsync") {
		t.Error("rsync darf nicht in der gemeinsamen Paketliste stehen (der Zeuge braucht es nicht)")
	}
	in := scriptText(t, "install-pdh-ha.sh")
	mustContain(t, "install-pdh-ha.sh", in, "rsync", "PDH_HA_WITNESS", "witness")
}

// TestRestoreRaeumtRewindMarke: pdh-restore.sh löscht nach dem Zurückspielen die Marke
// „<Datenordner>.rewind-laeuft“, sonst hielte pdh-ha die gute Datenbank für unsicher.
func TestRestoreRaeumtRewindMarke(t *testing.T) {
	s := scriptText(t, "pdh-restore.sh")
	rm := `rm -f "$DATA.rewind-laeuft"`
	mustContain(t, "pdh-restore.sh", s, rm)
	if strings.Index(s, rm) < strings.Index(s, `tar -xzf "$FROM/base/$NAME/base.tar.gz" -C "$DATA"`) {
		t.Error("die Marke muss nach dem Zurückspielen gelöscht werden")
	}
	if strings.Index(s, rm) > strings.Index(s, `systemctl start "postgresql@$PGVER-main"`) {
		t.Error("die Marke muss vor dem Start von Postgres verschwinden")
	}
}

// TestRewindTexteNennenSicherung: Alarm und Fehler nennen den Ausweg ohne Partner.
func TestRewindTexteNennenSicherung(t *testing.T) {
	for name, txt := range map[string]string{"rewindUnsafeText": rewindUnsafeText, "errDataUnsafe": errDataUnsafe.Error()} {
		if !strings.Contains(txt, "sudo bash scripts/pdh-restore.sh") {
			t.Errorf("%s nennt pdh-restore.sh nicht: %s", name, txt)
		}
	}
	if !strings.Contains(rewindUnsafeText, "sudo pdh-ha rejoin") {
		t.Error("rewindUnsafeText muss rejoin weiter nennen")
	}
}
