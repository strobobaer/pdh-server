package ha

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMirrorDefaults(t *testing.T) {
	d := DefaultSettings()
	if d.MirrorOn || d.MirrorNextcloud {
		t.Fatal("Spiegelung und Nextcloud müssen standardmäßig aus sein")
	}
	if d.MirrorEvery != time.Minute || d.MirrorTrashDays != 14 ||
		d.MirrorAlarmAfter != 5*time.Minute || d.MirrorFinalMax != 120*time.Second ||
		d.MirrorBWLimitKB != 0 {
		t.Fatalf("Standardwerte falsch: %+v", d)
	}
	if d.NCAppDir != "/var/www/nextcloud" || d.NCDataDir != "" {
		t.Fatalf("Nextcloud-Pfade falsch: %q %q", d.NCAppDir, d.NCDataDir)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Standard muss gültig sein: %v", err)
	}
}

func TestMirrorSettingsJSONOldFile(t *testing.T) {
	old := `{"tick_s":1,"fence_s":10,"takeover_trio_s":30,"takeover_duo_s":60,"cooldown_min":15,"max_per_day":3,"async_after_s":10,"lag_limit_mb":16,"slot_keep_gb":20,"auto_mode":"observe"}`
	var s Settings
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatal(err)
	}
	d := DefaultSettings()
	if s != d {
		t.Fatalf("alte Datei muss Standardwerte ergeben:\n%+v\n%+v", s, d)
	}
	// Rundreise mit gesetzten Werten.
	s.MirrorOn, s.MirrorNextcloud = true, true
	s.MirrorEvery, s.MirrorTrashDays = 90*time.Second, 30
	s.MirrorAlarmAfter, s.MirrorFinalMax = 10*time.Minute, 60*time.Second
	s.MirrorBWLimitKB, s.NCDataDir, s.NCAppDir = 2048, "/srv/nc-data", "/srv/nc"
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Settings
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != s {
		t.Fatalf("Rundreise ungleich:\n%+v\n%+v", back, s)
	}
	for _, k := range []string{"mirror_on", "mirror_every_s", "mirror_trash_days", "mirror_alarm_min", "mirror_final_s", "mirror_bwlimit_kb", "nc_data_dir", "nc_app_dir", "mirror_nextcloud"} {
		if !strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("JSON ohne %s: %s", k, b)
		}
	}
}

func TestMirrorValidate(t *testing.T) {
	ok := func(m func(*Settings)) Settings {
		s := DefaultSettings()
		m(&s)
		return s
	}
	bad := []struct {
		name string
		s    Settings
	}{
		{"Abstand zu klein", ok(func(s *Settings) { s.MirrorEvery = 29 * time.Second })},
		{"Abstand zu groß", ok(func(s *Settings) { s.MirrorEvery = 3601 * time.Second })},
		{"Papierkorb 0", ok(func(s *Settings) { s.MirrorTrashDays = 0 })},
		{"Papierkorb 366", ok(func(s *Settings) { s.MirrorTrashDays = 366 })},
		{"Alarm 0", ok(func(s *Settings) { s.MirrorAlarmAfter = 0 })},
		{"Alarm 1441", ok(func(s *Settings) { s.MirrorAlarmAfter = 1441 * time.Minute })},
		{"Frist 9", ok(func(s *Settings) { s.MirrorFinalMax = 9 * time.Second })},
		{"Frist 601", ok(func(s *Settings) { s.MirrorFinalMax = 601 * time.Second })},
		{"Bandbreite negativ", ok(func(s *Settings) { s.MirrorBWLimitKB = -1 })},
		{"NC ohne Datenordner", ok(func(s *Settings) { s.MirrorNextcloud = true })},
		{"NC relativ", ok(func(s *Settings) { s.MirrorNextcloud, s.NCDataDir = true, "daten" })},
		{"NC App relativ", ok(func(s *Settings) { s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/srv/d", "app" })},
	}
	for _, p := range []string{"/", "/etc", "/etc/pdh", "/etc/pdh/ha", "/var", "/opt", "/etc/pdh/ha/x", "/etc/pdh/ha/a/b", "/etc/pdh/ha/../ha/x"} {
		p := p
		bad = append(bad, struct {
			name string
			s    Settings
		}{"Pfad " + p, ok(func(s *Settings) { s.MirrorNextcloud, s.NCDataDir = true, p })},
			struct {
				name string
				s    Settings
			}{"Programmordner " + p, ok(func(s *Settings) { s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/srv/d", p })})
	}
	// Gesamtprüfung #3: gleicher Daten- und Programmordner ergäbe zwei Quellen auf ein Ziel
	bad = append(bad, struct {
		name string
		s    Settings
	}{"Datenordner = Programmordner", ok(func(s *Settings) { s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/srv/nc", "/srv/nc/" })})
	for _, c := range bad {
		if err := c.s.Validate(); err == nil {
			t.Errorf("%s: Fehler erwartet", c.name)
		}
	}
	good := ok(func(s *Settings) {
		s.MirrorOn, s.MirrorNextcloud, s.NCDataDir = true, true, "/srv/nc-data"
		s.MirrorBWLimitKB = 1000
	})
	if err := good.Validate(); err != nil {
		t.Errorf("gültige Einstellung abgelehnt: %v", err)
	}
	// Nachbarn von /etc/pdh/ha sind kein Teil davon
	near := ok(func(s *Settings) { s.MirrorNextcloud, s.NCDataDir, s.NCAppDir = true, "/etc/pdh/hadata", "/srv/nc" })
	if err := near.Validate(); err != nil {
		t.Errorf("/etc/pdh/hadata abgelehnt: %v", err)
	}
	// Ohne Nextcloud sind die Pfade frei; ungesetzte (0) Spiegelzeiten gelten als Standard.
	zero := DefaultSettings()
	zero.MirrorEvery, zero.MirrorTrashDays, zero.MirrorAlarmAfter, zero.MirrorFinalMax = 0, 0, 0, 0
	if err := zero.Validate(); err == nil {
		t.Log("ungesetzte Spiegelzeiten akzeptiert")
	} else {
		t.Errorf("ungesetzte Spiegelzeiten dürfen nicht stören: %v", err)
	}
}

func labels(src []MirrorSource) []string {
	var l []string
	for _, x := range src {
		l = append(l, x.Label)
	}
	return l
}

func TestMirrorSources(t *testing.T) {
	s := DefaultSettings()
	got := labels(MirrorSources("/opt/pdh", s))
	if strings.Join(got, ",") != "uploads,chat_files,etc-pdh" {
		t.Fatalf("ohne Nextcloud: %v", got)
	}
	s.MirrorNextcloud, s.NCDataDir = true, "/srv/nc-data"
	src := MirrorSources("/opt/pdh", s)
	got = labels(src)
	if strings.Join(got, ",") != "uploads,chat_files,etc-pdh,nextcloud-data,nextcloud-app,nginx,ssl-pdh-nextcloud,letsencrypt" {
		t.Fatalf("mit Nextcloud: %v", got)
	}
	instant := map[string]bool{"uploads": true, "chat_files": true, "nextcloud-data": true}
	for _, x := range src {
		if x.Instant != instant[x.Label] {
			t.Errorf("%s: Instant=%v", x.Label, x.Instant)
		}
	}
	has := func(x MirrorSource, e string) bool {
		for _, v := range x.Exclude {
			if v == e {
				return true
			}
		}
		return false
	}
	byL := func(l string) MirrorSource {
		x, ok := MirrorSourceByLabel("/opt/pdh", s, l)
		if !ok {
			t.Fatalf("Quelle %s fehlt", l)
		}
		return x
	}
	if !has(byL("etc-pdh"), "--exclude=/ha/") {
		t.Error("etc-pdh muss /ha/ ausschließen")
	}
	nd := byL("nextcloud-data")
	for _, e := range []string{"--exclude=/appdata_*/preview/", "--exclude=/*/uploads/", "--exclude=*.part", "--exclude=/nextcloud.log"} {
		if !has(nd, e) {
			t.Errorf("nextcloud-data ohne %s", e)
		}
	}
	if byL("uploads").Path != "/opt/pdh/uploads" {
		t.Errorf("uploads-Pfad: %s", byL("uploads").Path)
	}
	if byL("etc-pdh").Path != "/etc/pdh" {
		t.Errorf("etc-pdh-Pfad: %s", byL("etc-pdh").Path)
	}
	if _, ok := MirrorSourceByLabel("/opt/pdh", s, "gibtsnicht"); ok {
		t.Error("unbekannte Quelle gefunden")
	}
	// Datenordner innerhalb der App: wird von nextcloud-app ausgeschlossen.
	s.NCDataDir = "/var/www/nextcloud/data"
	app, _ := MirrorSourceByLabel("/opt/pdh", s, "nextcloud-app")
	if !has(app, "--exclude=/data/") {
		t.Errorf("nextcloud-app muss den Datenordner ausschließen: %v", app.Exclude)
	}
	// Der Papierkorb des Datenordners liegt im Programmordner und darf nie mitgespiegelt werden.
	if !has(app, "--exclude=/.pdh-papierkorb/") {
		t.Errorf("nextcloud-app muss .pdh-papierkorb ausschließen: %v", app.Exclude)
	}
}

func TestMirrorTrashDir(t *testing.T) {
	got := MirrorTrashDir("/var/www/nextcloud", "2026-11-03", "nextcloud-app")
	if got != "/var/www/.pdh-papierkorb/2026-11-03/nextcloud-app" {
		t.Fatalf("Papierkorb: %s", got)
	}
}

// R57: Jede Quelle trägt die Kennungsdatei .pdh-mirror-id und schließt sie von der Übertragung aus.
// Bei nextcloud-app liegt sie in config/, weil Nextclouds Integritätsprüfung fremde Dateien im
// Programmordner meldet (EXTRA_FILE) und config/ davon ausnimmt.
func TestMirrorSourcesIDFile(t *testing.T) {
	s := DefaultSettings()
	s.MirrorNextcloud, s.NCDataDir = true, "/srv/nc-data"
	for _, x := range MirrorSources("/opt/pdh", s) {
		want := MirrorIDName
		if x.Label == "nextcloud-app" {
			want = "config/" + MirrorIDName
		}
		if x.IDFile() != want {
			t.Errorf("%s: Kennungsdatei %q, erwartet %q", x.Label, x.IDFile(), want)
		}
		found := false
		for _, e := range x.Exclude {
			found = found || e == "--exclude=/"+want
		}
		if !found {
			t.Errorf("%s schließt die Kennung nicht aus: %v", x.Label, x.Exclude)
		}
	}
}
