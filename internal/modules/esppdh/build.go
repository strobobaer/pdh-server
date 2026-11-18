package esppdh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Bauen der Firmware mit ESPHome – ein Lauf, ohne Datenbank. Die Schlange (immer nur ein Lauf),
// das Speichern der Firmware und die Seite liegen in internal/web/esppdh_build.go.
//
// Ablauf je Lauf (Runner.Build):
//  1. freien Platz für den Arbeitsordner prüfen (MinFreeMB) – sonst sofort Fehler;
//  2. Arbeitsordner <WorkDir>/build/<lauf> mit Rechten 0700 anlegen, darin <kennung>.yaml und
//     secrets.yaml (0600);
//  3. „nice -n 19 <venv>/bin/esphome compile <kennung>.yaml“ in diesem Ordner, mit Zeitlimit
//     und eigener Prozessgruppe (build_unix.go): Bei Zeitlimit oder Abbruch endet auch jeder
//     Kindprozess (PlatformIO, Compiler);
//  4. firmware.factory.bin (erstes Flashen ab Adresse 0) und firmware.ota.bin (Update über das
//     Netz) lesen. ESPHome 2026.10.2 legt beide unter <ordner>/.esphome/build/<name>/ ab – je
//     nach Werkzeugkette in build/ (ESP-IDF) oder .pioenvs/<name>/ (PlatformIO, Arduino für
//     ESP8266); gesucht wird deshalb im ganzen Arbeitsordner;
//  5. den Arbeitsordner löschen – immer, auch bei Fehler, Zeitlimit, Abbruch und panic. Er
//     enthält neben secrets.yaml auch den erzeugten Quelltext mit den Kennwörtern.
//
// Bleiben dürfen nur die Werkzeuge: PlatformIO unter <WorkDir>/platformio (PLATFORMIO_CORE_DIR),
// die eigenen Werkzeugketten von ESPHome (ESP-IDF, Arduino für ESP8266) unter <WorkDir>/cache
// (XDG_CACHE_HOME). So lädt nur der erste Bau je Platinentyp aus dem Internet. ccache ist aus –
// sein Zwischenspeicher hielte übersetzte Teile mit den Kennwörtern außerhalb des Arbeitsordners.
//
// Der Verlauf geht geschwärzt hinaus (Redact): alle 2 s als Zwischenstand und am Ende.

// MaxLog: größter Verlauf in Byte (Anfang und Ende bleiben, siehe Redact).
const MaxLog = 256 << 10

const (
	factoryName = "firmware.factory.bin"
	otaName     = "firmware.ota.bin"
	maxFirmware = 32 << 20        // größer ist keine Firmware eines ESP
	waitDelay   = 5 * time.Second // hängende Ausgabe von Kindprozessen hält das Ende nicht auf
)

var (
	// progressEvery: Abstand der Zwischenstände (Tests verkürzen ihn).
	progressEvery = 2 * time.Second
	// freeSpaceMB: freier Platz in MB, -1 = unbekannt (Tests ersetzen ihn).
	freeSpaceMB = freeMB

	buildIDRe     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	versionLineRe = regexp.MustCompile(`(?m)^INFO ESPHome (\d+\.\d+\.\d+[0-9A-Za-z.\-]*)\s*$`)
	ansiRe        = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

// BuildJob: ein Bauauftrag. ID ist die Kennung des Laufs (UUID, Name des Arbeitsordners),
// Slug die Kennung des Geräts (Dateiname <slug>.yaml). Config und Secrets kommen aus Generate;
// SecretValues sind die Kennwörter im Klartext – sie werden im Verlauf geschwärzt.
type BuildJob struct {
	ID, Slug        string
	Config, Secrets []byte
	SecretValues    []string
}

// BuildResult: Ergebnis eines Laufs. Log ist geschwärzt und höchstens MaxLog groß. Factory ist
// das Abbild zum ersten Flashen (ab Adresse 0), OTA das für Updates über das Netz. Version: die
// ESPHome-Version laut Verlauf ("" = nicht erkannt).
type BuildResult struct {
	OK           bool
	Log          string
	Factory, OTA []byte
	Version      string
}

// Runner baut eine Firmware. Command baut den Aufruf von ESPHome im Ordner dir (Tests setzen
// eine Attrappe ein); Umgebung, Ausgabe und Prozessgruppe setzt Build.
type Runner struct {
	Timeout   time.Duration
	MinFreeMB int64
	Command   func(ctx context.Context, dir string, args ...string) *exec.Cmd
}

// NewRunner: Zeitlimit 30 min, mindestens 2048 MB frei, Aufruf „nice -n 19 <venv>/bin/esphome“.
func NewRunner() *Runner {
	return &Runner{Timeout: 30 * time.Minute, MinFreeMB: 2048, Command: esphomeCommand}
}

// esphomeCommand: „nice -n 19 <venv>/bin/esphome <args>“ im Ordner dir – niedrige Priorität,
// damit ein Bau das PDH nicht bremst (ohne nice, falls es fehlt).
func esphomeCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	bin := esphomeBin()
	name, argv := bin, args
	if nice, err := exec.LookPath("nice"); err == nil {
		name, argv = nice, append([]string{"-n", "19", bin}, args...)
	}
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Dir = dir
	return cmd
}

// Build führt einen Lauf aus (siehe Kopf der Datei). progress (darf nil sein) bekommt alle 2 s
// den geschwärzten Verlauf bis zur letzten vollständigen Zeile – nie nach dem Ende von Build.
// Abbruch über ctx (Knopf „Abbrechen“, Herunterfahren); das Zeitlimit setzt Build selbst.
func (r *Runner) Build(ctx context.Context, job BuildJob, progress func(log string)) (res BuildResult) {
	if !buildIDRe.MatchString(job.ID) || !ValidSlug(job.Slug) {
		return BuildResult{Log: "Ungültiger Bauauftrag – nichts gebaut."}
	}
	root := workDir()
	if free := freeSpaceMB(root); free >= 0 && free < r.MinFreeMB {
		return BuildResult{Log: fmt.Sprintf("Zu wenig Platz für den Bau: frei sind %d MB, nötig sind mindestens %d MB (Ordner %s). Bitte Platz schaffen und neu bauen.",
			free, r.MinFreeMB, root)}
	}
	dir, err := prepareBuildDir(root, job)
	defer func() { // immer aufräumen – auch bei Fehler, Zeitlimit, Abbruch und panic
		if dir == "" {
			return
		}
		if err := removeBuildDir(dir); err != nil {
			res.Log = Redact(res.Log+"\nDer Arbeitsordner ließ sich nicht löschen: "+err.Error(), job.SecretValues)
		}
	}()
	if err != nil {
		return BuildResult{Log: "Der Arbeitsordner lässt sich nicht anlegen: " + err.Error()}
	}
	return r.run(ctx, dir, job, progress)
}

// prepareBuildDir legt Werkzeug-Ordner und den Arbeitsordner des Laufs (0700) mit beiden
// Dateien an. Rückgabe auch bei Fehler: der Ordner, sobald er angelegt ist (zum Aufräumen).
func prepareBuildDir(root string, job BuildJob) (string, error) {
	for _, d := range []string{filepath.Join(root, "build"), filepath.Join(root, "platformio"), filepath.Join(root, "cache")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return "", err
		}
	}
	dir := filepath.Join(root, "build", job.ID)
	if err := os.RemoveAll(dir); err != nil { // Rest eines abgebrochenen Laufs derselben Kennung
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return dir, err
	}
	if err := os.WriteFile(filepath.Join(dir, job.Slug+".yaml"), job.Config, 0o600); err != nil {
		return dir, err
	}
	return dir, os.WriteFile(filepath.Join(dir, "secrets.yaml"), job.Secrets, 0o600)
}

// removeBuildDir löscht den Arbeitsordner, die Geheimnis-Datei zuerst. Unter Windows halten eben
// beendete Prozesse Dateien noch kurz fest – deshalb einige Versuche.
func removeBuildDir(dir string) error {
	_ = os.Remove(filepath.Join(dir, "secrets.yaml"))
	var err error
	for i := 0; i < 20; i++ {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// buildEnv: Umgebung für ESPHome – Werkzeuge dauerhaft unter WorkDir, alles mit Kennwörtern
// im Arbeitsordner (ESPHOME_DATA_DIR/ESPHOME_BUILD_PATH fest, falls der Dienst sie anders setzt),
// kein ccache, Ausgabe ohne Puffer (für den Zwischenstand).
func buildEnv(base []string, dir string) []string {
	if base == nil {
		base = os.Environ()
	}
	root := workDir()
	return append(slices.Clip(base),
		"PLATFORMIO_CORE_DIR="+filepath.Join(root, "platformio"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"ESPHOME_DATA_DIR="+filepath.Join(dir, ".esphome"),
		"ESPHOME_BUILD_PATH=build",
		"ESPHOME_CCACHE_ENABLE=0",
		"IDF_CCACHE_ENABLE=0",
		"PYTHONUNBUFFERED=1",
	)
}

// run startet ESPHome im Arbeitsordner und wertet das Ende aus.
func (r *Runner) run(ctx context.Context, dir string, job BuildJob, progress func(string)) BuildResult {
	tctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	lg := &buildLog{}
	cmd := r.Command(tctx, dir, "compile", job.Slug+".yaml")
	if cmd.Dir == "" {
		cmd.Dir = dir
	}
	cmd.Env = buildEnv(cmd.Env, dir)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = lg, lg
	cmd.WaitDelay = waitDelay
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return BuildResult{Log: Redact("ESPHome lässt sich nicht starten: "+err.Error(), job.SecretValues)}
	}
	// Zeitlimit oder Abbruch: die ganze Prozessgruppe beenden
	stopKill := context.AfterFunc(tctx, func() { killGroup(cmd) })

	// Zwischenstand alle 2 s (nur wenn neue Ausgabe da ist)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	if progress != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(progressEvery)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if lg.takeChanged() {
						progress(Redact(cleanLog(lg.snapshot(true)), job.SecretValues))
					}
				}
			}
		}()
	}

	err := cmd.Wait()
	stopKill()
	close(stop)
	wg.Wait()
	killGroup(cmd) // Kindprozesse, die das Ende von ESPHome überlebt haben
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil // ESPHome ist fertig; nur ein Kindprozess hielt die Ausgabe noch offen
	}

	out := cleanLog(lg.snapshot(false))
	res := BuildResult{}
	if m := versionLineRe.FindStringSubmatch(out); m != nil {
		res.Version = m[1]
	}
	var status string
	switch {
	case ctx.Err() != nil:
		status = "Bau abgebrochen."
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		status = "Zeitlimit von " + durText(r.Timeout) + " überschritten – Bau abgebrochen."
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			status = fmt.Sprintf("ESPHome hat mit einem Fehler beendet (Exit-Code %d).", ee.ExitCode())
		} else {
			status = "ESPHome ist mit einem Fehler beendet: " + err.Error()
		}
	default:
		factory, ota, ferr := readFirmware(dir)
		if ferr != nil {
			status = ferr.Error()
			break
		}
		res.OK, res.Factory, res.OTA = true, factory, ota
		status = fmt.Sprintf("Firmware gebaut (%s, für Updates über das Netz %s).", sizeText(len(factory)), sizeText(len(ota)))
	}
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	res.Log = Redact(out+status+"\n", job.SecretValues)
	return res
}

// readFirmware sucht firmware.factory.bin und firmware.ota.bin im Arbeitsordner (nur
// gewöhnliche Dateien; bei mehreren die jüngste).
func readFirmware(dir string) (factory, ota []byte, err error) {
	paths := map[string]string{}
	mod := map[string]time.Time{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || (d.Name() != factoryName && d.Name() != otaName) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if t, ok := mod[d.Name()]; !ok || info.ModTime().After(t) {
			paths[d.Name()], mod[d.Name()] = p, info.ModTime()
		}
		return nil
	})
	if paths[factoryName] == "" || paths[otaName] == "" {
		return nil, nil, errors.New("ESPHome hat keine vollständige Firmware erzeugt (" + factoryName + " und " + otaName + " erwartet).")
	}
	read := func(p string) ([]byte, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxFirmware+1))
		switch {
		case err != nil:
			return nil, err
		case len(b) == 0 || len(b) > maxFirmware:
			return nil, fmt.Errorf("%s hat eine unmögliche Größe (%d Byte)", filepath.Base(p), len(b))
		}
		return b, nil
	}
	if factory, err = read(paths[factoryName]); err == nil {
		ota, err = read(paths[otaName])
	}
	if err != nil {
		return nil, nil, errors.New("Die Firmware lässt sich nicht lesen: " + err.Error())
	}
	return factory, ota, nil
}

// CleanupBuilds löscht alle Arbeitsordner unter <WorkDir>/build – Reste eines Laufs, den ein
// Absturz oder Neustart unterbrochen hat (sie enthalten Kennwörter). Nur aufrufen, solange kein
// Bau läuft (beim Start). Die Werkzeuge bleiben.
func CleanupBuilds() error {
	dir := filepath.Join(workDir(), "build")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var first error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ── Verlauf ──────────────────────────────────────────────────

const (
	logHeadMax = 128 << 10 // Anfang (Version, Konfiguration)
	logTailMax = 1 << 20   // Ende (Fehlermeldungen)
)

// buildLog sammelt die Ausgabe von ESPHome mit fester Obergrenze: Anfang und Ende bleiben,
// dazwischen wird ausgelassen – an Zeilengrenzen, damit kein Kennwort zerschnitten wird und so
// dem Schwärzen entgeht.
type buildLog struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	full    bool  // Anfang voll, alles Weitere geht ans Ende
	tailMid bool  // das Ende beginnt mitten in einer Zeile (ohne Zeilenende abgeschnitten)
	dropped int64 // ausgelassene Byte
	changed bool
}

func (l *buildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(p)
	l.changed = true
	if !l.full {
		if room := logHeadMax - len(l.head); len(p) <= room {
			l.head = append(l.head, p...)
			return n, nil
		}
		l.head = append(l.head, p...)
		l.full = true
		cut := logHeadMax
		if i := bytes.LastIndexByte(l.head[:logHeadMax], '\n'); i >= 0 {
			cut = i + 1
		}
		p = append([]byte(nil), l.head[cut:]...)
		l.head = l.head[:cut:cut]
	}
	l.tail = append(l.tail, p...)
	if len(l.tail) > 2*logTailMax { // selten kürzen: jedes Kürzen kopiert bis zu 1 MiB
		over := len(l.tail) - logTailMax
		cut := over
		if i := bytes.IndexByte(l.tail[over:], '\n'); i >= 0 {
			cut, l.tailMid = over+i+1, false
		} else {
			l.tailMid = true
		}
		l.dropped += int64(cut)
		l.tail = append([]byte(nil), l.tail[cut:]...)
	}
	return n, nil
}

// takeChanged: neue Ausgabe seit dem letzten Aufruf?
func (l *buildLog) takeChanged() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.changed
	l.changed = false
	return c
}

// snapshot: bisherige Ausgabe; complete = nur bis zur letzten vollständigen Zeile (ein halb
// geschriebenes Kennwort würde sonst nicht erkannt).
func (l *buildLog) snapshot(complete bool) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	b.Write(l.head)
	tail := l.tail
	if l.dropped > 0 {
		if l.tailMid {
			if i := bytes.IndexByte(tail, '\n'); i >= 0 {
				tail = tail[i+1:]
			} else {
				tail = nil
			}
		}
		fmt.Fprintf(&b, "\n… Ausgabe gekürzt: %d KiB ausgelassen …\n", l.dropped>>10)
	}
	b.Write(tail)
	s := b.String()
	if complete {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[:i+1]
		} else {
			s = ""
		}
	}
	return s
}

// cleanLog macht die Ausgabe speicher- und lesbar: gültiges UTF-8 ohne NUL (PostgreSQL),
// ohne Farbcodes, Fortschrittsbalken (Wagenrücklauf) nur mit dem letzten Stand.
func cleanLog(s string) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "�")
	s = ansiRe.ReplaceAllString(s, "")
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if j := strings.LastIndexByte(l, '\r'); j >= 0 {
			lines[i] = l[j+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// Redact ersetzt jedes Geheimnis (ab 4 Zeichen; kürzere würden den Verlauf verstümmeln) durch
// „•••“ – auch so, wie es ESPHome in den C++-Quelltext schreibt und der Compiler es in einer
// Fehlermeldung zitiert (cpp_string_escape: „"“, „\“ und alles außer druckbarem ASCII oktal),
// und mit Backslash maskiert (YAML/JSON in doppelten Anführungszeichen). Danach höchstens MaxLog
// Byte: Anfang und Ende bleiben, dazwischen eine Hinweiszeile „… gekürzt …“.
func Redact(log string, secrets []string) string {
	var olds []string
	seen := map[string]bool{}
	for _, s := range secrets {
		if utf8.RuneCountInString(s) < 4 {
			continue
		}
		for _, v := range []string{s, cppEscape(s), backslashEscape(s)} {
			if !seen[v] {
				seen[v] = true
				olds = append(olds, v)
			}
		}
	}
	sort.SliceStable(olds, func(i, j int) bool { return len(olds[i]) > len(olds[j]) }) // längere zuerst
	for _, o := range olds {
		log = strings.ReplaceAll(log, o, "•••")
	}
	return limitLog(log)
}

// cppEscape: Schreibweise in main.cpp (esphome.helpers.cpp_string_escape, ohne Anführungszeichen).
func cppEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 32 || c >= 127 || c == '\\' || c == '"' {
			fmt.Fprintf(&b, "\\%03o", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// backslashEscape: „\“ und „"“ mit Backslash maskiert (YAML und JSON in doppelten Anführungszeichen).
func backslashEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// limitLog kürzt auf MaxLog Byte: ein Viertel Anfang, der Rest Ende, an Zeilengrenzen.
func limitLog(s string) string {
	if len(s) <= MaxLog {
		return s
	}
	const headMax = MaxLog / 4
	head := s[:headMax]
	if i := strings.LastIndexByte(head, '\n'); i >= 0 {
		head = head[:i+1]
	} else {
		head = s[:runeStart(s, headMax)]
	}
	note := "… gekürzt: " + strconv.Itoa((len(s)-MaxLog)>>10+1) + " KiB in der Mitte ausgelassen …\n"
	if !strings.HasSuffix(head, "\n") {
		note = "\n" + note
	}
	from := len(s) - (MaxLog - len(head) - len(note))
	if i := strings.IndexByte(s[from:], '\n'); i >= 0 && i+1 < len(s)-from {
		from += i + 1 // an einer Zeilengrenze beginnen
	} else {
		from = runeStart(s, from) // innerhalb eines Zeichens beginnen hieße ungültiges UTF-8
	}
	return head + note + s[from:]
}

// runeStart: erste Zeichengrenze ab i.
func runeStart(s string, i int) int {
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return i
}

// durText: „30 min“ oder „0,3 s“.
func durText(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return strconv.Itoa(int(d/time.Minute)) + " min"
	}
	return strings.Replace(strconv.FormatFloat(d.Seconds(), 'f', -1, 64), ".", ",", 1) + " s"
}

// sizeText: „812 KB“ bzw. „1,2 MB“.
func sizeText(n int) string {
	if n < 1<<20 {
		return strconv.Itoa((n+1023)>>10) + " KB"
	}
	return strings.Replace(strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64), ".", ",", 1) + " MB"
}
