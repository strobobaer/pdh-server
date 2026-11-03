package ha

import (
	"errors"
	"path"
	"strings"
	"time"
)

// MirrorSource ist ein Ordner, der auf den Standby gespiegelt wird.
// Instant-Quellen werden fortlaufend nachgezogen, die übrigen im Abstand
// Settings.MirrorEvery. Exclude enthält fertige rsync-Argumente.
type MirrorSource struct {
	Label, Path string
	Instant     bool
	Exclude     []string
}

// mirrorForbidden sind Ordner, die nie als Nextcloud-Pfad taugen (zu weit
// oben im System oder der Ausfallschutz selbst).
var mirrorForbidden = map[string]bool{
	"/": true, "/etc": true, "/etc/pdh": true, "/etc/pdh/ha": true, "/var": true, "/opt": true,
}

func checkMirrorDir(name, p string) error {
	if !path.IsAbs(p) {
		return errors.New(name + " muss ein absoluter Pfad sein")
	}
	if mirrorForbidden[path.Clean(p)] {
		return errors.New(name + " darf nicht " + path.Clean(p) + " sein")
	}
	return nil
}

// validateMirror prüft die Spiegel-Einstellungen. Sind alle vier Zeitfelder 0
// (von Hand gebaute oder nie gesetzte Einstellungen), gelten sie als
// „ungesetzt“ und werden nicht geprüft; die Spiegelung selbst nutzt dann die
// Standardwerte.
func (s Settings) validateMirror() error {
	if s.MirrorEvery != 0 || s.MirrorTrashDays != 0 || s.MirrorAlarmAfter != 0 || s.MirrorFinalMax != 0 {
		if s.MirrorEvery < 30*time.Second || s.MirrorEvery > 3600*time.Second {
			return errors.New("Abstand der Datei-Spiegelung muss zwischen 30 und 3600 s liegen")
		}
		if s.MirrorTrashDays < 1 || s.MirrorTrashDays > 365 {
			return errors.New("Papierkorb-Aufbewahrung muss zwischen 1 und 365 Tagen liegen")
		}
		if s.MirrorAlarmAfter < time.Minute || s.MirrorAlarmAfter > 1440*time.Minute {
			return errors.New("Alarm der Datei-Spiegelung muss zwischen 1 und 1440 min liegen")
		}
		if s.MirrorFinalMax < 10*time.Second || s.MirrorFinalMax > 600*time.Second {
			return errors.New("Frist des letzten Abgleichs muss zwischen 10 und 600 s liegen")
		}
	}
	if s.MirrorBWLimitKB < 0 {
		return errors.New("Bandbreite der Datei-Spiegelung darf nicht negativ sein")
	}
	if s.MirrorNextcloud {
		if s.NCDataDir == "" {
			return errors.New("Nextcloud-Datenordner fehlt")
		}
		if err := checkMirrorDir("Nextcloud-Datenordner", s.NCDataDir); err != nil {
			return err
		}
		if s.NCAppDir == "" {
			return errors.New("Nextcloud-Programmordner fehlt")
		}
		if err := checkMirrorDir("Nextcloud-Programmordner", s.NCAppDir); err != nil {
			return err
		}
	}
	return nil
}

// MirrorSources liefert die zu spiegelnden Quellen. repo ist der Ordner der
// PDH-Installation. /etc/pdh/ha/ (Schlüssel und Zustand des Ausfallschutzes)
// wird nie gespiegelt.
func MirrorSources(repo string, s Settings) []MirrorSource {
	src := []MirrorSource{
		{Label: "uploads", Path: path.Join(repo, "uploads"), Instant: true},
		{Label: "chat_files", Path: path.Join(repo, "chat_files"), Instant: true},
		{Label: "etc-pdh", Path: "/etc/pdh", Exclude: []string{"--exclude=/ha/"}},
	}
	if !s.MirrorNextcloud {
		return src
	}
	// Der Papierkorb des Datenordners liegt im Elternordner des Ziels, also hier.
	appEx := []string{"--exclude=/.pdh-papierkorb/"}
	// Liegt der Datenordner im Programmordner, gehört er nicht in diese Quelle.
	if rel := strings.TrimPrefix(path.Clean(s.NCDataDir), path.Clean(s.NCAppDir)+"/"); rel != path.Clean(s.NCDataDir) {
		appEx = append(appEx, "--exclude=/"+rel+"/")
	}
	return append(src,
		MirrorSource{Label: "nextcloud-data", Path: s.NCDataDir, Instant: true, Exclude: []string{
			"--exclude=/appdata_*/preview/", "--exclude=/*/uploads/", "--exclude=*.part", "--exclude=/nextcloud.log"}},
		MirrorSource{Label: "nextcloud-app", Path: s.NCAppDir, Exclude: appEx},
		MirrorSource{Label: "nginx", Path: "/etc/nginx"},
		MirrorSource{Label: "ssl-pdh-nextcloud", Path: "/etc/ssl/pdh-nextcloud"},
		MirrorSource{Label: "letsencrypt", Path: "/etc/letsencrypt"},
	)
}

// MirrorSourceByLabel sucht eine Quelle nach ihrem Namen.
func MirrorSourceByLabel(repo string, s Settings, label string) (MirrorSource, bool) {
	for _, m := range MirrorSources(repo, s) {
		if m.Label == label {
			return m, true
		}
	}
	return MirrorSource{}, false
}

// MirrorTrashDir ist der Papierkorb einer Quelle auf dem Standby:
// <Elternordner des Ziels>/.pdh-papierkorb/<JJJJ-MM-TT>/<Label>.
func MirrorTrashDir(target, day, label string) string {
	return path.Join(path.Dir(path.Clean(target)), ".pdh-papierkorb", day, label)
}
