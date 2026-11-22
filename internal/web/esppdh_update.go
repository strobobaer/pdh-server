package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"pdh"
	"pdh/internal/modules/esppdh"
)

// ESPPDH – ESPHome hält sich selbst aktuell (Entwurf docs/superpowers/specs/2026-11-19-esphome-
// aktuell-design.md). Jeder Server prüft einmal am Tag selbst (StartESPPDHUpdate, auch am Spiegel):
//
//  1. PyPI nach der neuesten stabilen, ausgereiften Version fragen (esppdh.PickCandidate);
//  2. der Update-Agent richtet sie neben der laufenden ein (POST /v1/esppdh/install mit Version);
//  3. Probe mit dem Programm des Kandidaten: „esphome config“ für alle Musterkonfigurationen und
//     alle nicht gesperrten Geräte, am aktiven Server dazu ein echter Probebau je Chip-Familie;
//  4. bestanden: der Agent schaltet um; gescheitert: Kandidat weg, Version vermerkt (erst nach
//     einem PDH-Update oder über „Jetzt prüfen“ wieder versucht), Meldung an esppdh.admin.
//
// Firmware wird dabei nie gebaut, gespeichert oder verteilt. Der Zustand steht je Server in einer
// Datei im Arbeitsordner (esppdh.UpdateState) – am Spiegel ist die Datenbank nur lesbar. Nie
// laufen zwei ESPHome-Aufrufe zugleich: Bau und Probe teilen sich esppdhQ.runMu.

// esppdhSetAuto: Schalter „ESPHome selbst aktuell halten“ in app_settings – nur "0" heißt aus.
const esppdhSetAuto = "esppdh.autoupdate"

const (
	esppdhPyPIDefault = "https://pypi.org/pypi/esphome/json"
	esppdhPyPIMax     = 4 << 20 // größte angenommene Antwort von PyPI
	esppdhFailLines   = 40      // so viele Zeilen der Ausgabe bleiben bei einem Fehlschlag
	esppdhFailMax     = 16 << 10
	esppdhSkipNames   = 5 // so viele übersprungene Geräte nennt das Ergebnis
	// esppdhSwitchTimeout: Umschalten und Verwerfen beim Agenten – er ruft „esphome version“ auf
	// (bis 60 s) und löscht alte Umgebungen
	esppdhSwitchTimeout = 2 * time.Minute
	// esppdhCandFreeMB: unter so viel freiem Platz (MB) wird keine neue Version eingerichtet
	esppdhCandFreeMB = 3072
)

// Wartezeiten des Ablaufs (Tests verkürzen sie).
var (
	esppdhPyPITimeout  = 20 * time.Second // Anfrage an PyPI
	esppdhInstallPoll  = 5 * time.Second  // Stand der Einrichtung beim Agenten abfragen
	esppdhInstallLimit = 25 * time.Minute // höchstens so lange auf die Einrichtung warten
	esppdhIdleWait     = 30 * time.Second // vor dem Probebau: so oft nach der Bau-Schlange sehen
	esppdhIdleLimit    = 60 * time.Minute // … und höchstens so lange warten
	esppdhUpdTick      = 5 * time.Minute  // Takt des täglichen Laufs
	esppdhUpdDelay     = 10 * time.Minute // erste Prüfung frühestens so lange nach dem Start
	esppdhUpdDue       = 24 * time.Hour   // fällig, wenn die letzte Prüfung so lange her ist
	// esppdhStopGrace: Nach einem Fehler von ESPHome so lange abwarten, ob das PDH gerade beendet
	// wird. systemd beendet ESPHome zusammen mit dem PDH – der Fehler kommt dann vor dem Abbruch.
	esppdhStopGrace = 3 * time.Second
)

// esppdhCheckEnv: Zustand der laufenden Umgebung (ruft „esphome version“; Tests ersetzen ihn).
var esppdhCheckEnv = esppdh.CheckEnv

// esppdhFreeMB: freier Platz in MB für einen Ordner (-1 = unbekannt; Tests ersetzen es).
var esppdhFreeMB = esppdh.FreeMBAt

const (
	esppdhMsgUpdBusy  = "Eine Prüfung läuft schon"
	esppdhMsgAgentOld = "Der Update-Agent ist zu alt – zuerst das PDH aktualisieren"
	esppdhMsgVertagt  = "Probe vertagt – Bau-Schlange belegt"
	esppdhMsgStopped  = "Prüfung abgebrochen – das PDH wurde beendet"
	esppdhMsgNoSpace  = "Zu wenig Platz für eine neue ESPHome-Version (unter 3 GB frei)"
)

// errEsppdhQueueBusy: Die Probe hat nicht auf die Bau-Schlange gewartet – sie blieb zu lange
// belegt, oder das PDH wird beendet.
var errEsppdhQueueBusy = errors.New("bau-schlange belegt")

// esppdhProbeRunner prüft und baut mit einem bestimmten ESPHome-Programm (esppdh.Runner; Tests
// setzen eine Attrappe ein).
type esppdhProbeRunner interface {
	Config(ctx context.Context, job esppdh.BuildJob) (string, error)
	Build(ctx context.Context, job esppdh.BuildJob, progress func(string)) esppdh.BuildResult
}

// esppdhOutcome: Ausgang eines Schritts (Einrichten, Probe).
type esppdhOutcome int

const (
	esppdhPassed  esppdhOutcome = iota
	esppdhFailed                // an der Version gescheitert: vermerken und melden
	esppdhStopped               // abgebrochen ohne Vermerk – der nächste Lauf versucht es erneut
)

// esppdhAutoUpdate: Schalter „ESPHome selbst aktuell halten“ (Standard an).
func (h *Handler) esppdhAutoUpdate(ctx context.Context) bool {
	if h.db == nil {
		return true
	}
	return h.appSetting(ctx, esppdhSetAuto, "1") != "0"
}

func (h *Handler) esppdhClock() time.Time {
	if h.esppdhNow != nil {
		return h.esppdhNow()
	}
	return time.Now()
}

// esppdhProberFor: Prüfer mit dem Programm bin.
func (h *Handler) esppdhProberFor(bin string) esppdhProbeRunner {
	if h.esppdhProber != nil {
		return h.esppdhProber(bin)
	}
	r := esppdh.NewRunner()
	r.Bin = bin
	return r
}

// ── Takt ─────────────────────────────────────────────────────

// StartESPPDHUpdate startet den täglichen Lauf (Takt 5 min, erste Prüfung frühestens 10 min nach
// dem Start). Auf jedem Server, auch am Spiegel; nicht im Docker-Betrieb und nicht ohne Datenbank.
// Der Lauf endet mit ctx oder mit StopESPPDHUpdate.
func (h *Handler) StartESPPDHUpdate(ctx context.Context) {
	if h.db == nil || inDocker() {
		return
	}
	lctx, cancel := context.WithCancel(ctx)
	stopAfter := context.AfterFunc(h.esppdhUpdContext(), cancel)
	go func() {
		defer cancel()
		defer stopAfter()
		h.esppdhUpdateLoop(lctx)
	}()
}

// esppdhUpdContext: Kontext für den täglichen Lauf und „Jetzt prüfen“. Er hängt an keiner
// Anfrage (eine Prüfung dauert Minuten), endet aber mit StopESPPDHUpdate.
func (h *Handler) esppdhUpdContext() context.Context {
	u := &h.esppdhUpd
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.ctx == nil {
		u.ctx, u.stop = context.WithCancel(context.Background())
	}
	return u.ctx
}

// StopESPPDHUpdate bricht beim Herunterfahren den täglichen Lauf und eine laufende Prüfung ab –
// vor srv.Shutdown aufrufen. Eine so abgebrochene Prüfung zählt nicht gegen die geprüfte Version
// und nicht als „geprüft“; den halb eingerichteten Kandidaten räumt der Update-Agent auf.
func (h *Handler) StopESPPDHUpdate() {
	h.esppdhUpdContext() // legt den Kontext an, falls es noch keinen gibt
	h.esppdhUpd.mu.Lock()
	stop := h.esppdhUpd.stop
	h.esppdhUpd.mu.Unlock()
	stop()
}

// esppdhStopping: Wird das PDH gerade beendet? Nach einem Fehler von ESPHome wartet es dafür kurz
// (esppdhStopGrace): systemd beendet ESPHome zugleich mit dem PDH, der Fehler kommt dann vor dem
// Abbruch – ohne die Wartezeit würde eine gute Version als gescheitert gelten.
func esppdhStopping(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	t := time.NewTimer(esppdhStopGrace)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

func (h *Handler) esppdhUpdateLoop(ctx context.Context) {
	lg := componentLog("esppdh")
	t := time.NewTimer(esppdhUpdDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if h.esppdhUpdateDue() {
			msg, err := h.esppdhUpdateRun(ctx, false)
			if err != nil {
				lg.Error().Err(err).Str("ergebnis", msg).Msg("esphome-prüfung")
			} else {
				lg.Debug().Str("ergebnis", msg).Msg("esphome-prüfung")
			}
		}
		t.Reset(esppdhUpdTick)
	}
}

// esppdhUpdateDue: letzte Prüfung älter als 24 h (oder nie, oder die Uhr ging deutlich zurück).
// Es zählt die spätere aus Zustandsdatei und Speicher – lässt sich die Datei nicht schreiben,
// läuft die Prüfung trotzdem nicht alle 5 min.
func (h *Handler) esppdhUpdateDue() bool {
	last := esppdh.LoadUpdateState().CheckedAt
	h.esppdhUpd.mu.Lock()
	if h.esppdhUpd.lastRun.After(last) {
		last = h.esppdhUpd.lastRun
	}
	h.esppdhUpd.mu.Unlock()
	d := h.esppdhClock().Sub(last)
	return last.IsZero() || d >= esppdhUpdDue || d < -time.Hour
}

// ── Ablauf ───────────────────────────────────────────────────

// esppdhUpdateRun prüft, probt und schaltet um (siehe Kopf der Datei). force: „Jetzt prüfen“ –
// auch bei ausgeschaltetem Schalter, und eine gescheiterte Version wird erneut versucht.
// Höchstens ein Lauf zur Zeit. msg: Ergebnis in Klartext (auch, warum nichts geschah); err nur,
// wenn sich der Zustand nicht speichern ließ oder die Prüfung abstürzte.
func (h *Handler) esppdhUpdateRun(ctx context.Context, force bool) (msg string, err error) {
	if !h.esppdhUpdateBegin() {
		return esppdhMsgUpdBusy, nil
	}
	return h.esppdhUpdateRunning(ctx, force)
}

// esppdhUpdateBegin meldet einen Lauf an (false: es läuft schon einer). Wer true bekommt, muss
// esppdhUpdateRunning aufrufen – das gibt den Lauf am Ende wieder frei. So kann „Jetzt prüfen“
// den Lauf anmelden, bevor es antwortet, und im Hintergrund ausführen.
func (h *Handler) esppdhUpdateBegin() bool {
	u := &h.esppdhUpd
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return false
	}
	u.running = true
	return true
}

// esppdhUpdateEnd gibt einen mit esppdhUpdateBegin angemeldeten Lauf frei, ohne ihn auszuführen
// (z. B. beim Zurückstellen, das den Agenten nicht neben einer Prüfung bedienen soll).
func (h *Handler) esppdhUpdateEnd() {
	h.esppdhUpd.mu.Lock()
	h.esppdhUpd.running = false
	h.esppdhUpd.mu.Unlock()
}

// esppdhUpdateChecking: läuft gerade eine Prüfung?
func (h *Handler) esppdhUpdateChecking() bool {
	h.esppdhUpd.mu.Lock()
	defer h.esppdhUpd.mu.Unlock()
	return h.esppdhUpd.running
}

// esppdhUpdateRunning führt den mit esppdhUpdateBegin angemeldeten Lauf aus.
func (h *Handler) esppdhUpdateRunning(ctx context.Context, force bool) (msg string, err error) {
	u := &h.esppdhUpd
	defer func() {
		u.mu.Lock()
		u.running = false
		u.mu.Unlock()
	}()
	lg := componentLog("esppdh")
	var st esppdh.UpdateState
	started := false
	// Ein begonnener Lauf zählt als Prüfung – auch wenn er abstürzt oder sich der Zustand nicht
	// speichern lässt (lastRun im Speicher). Läuft im Hintergrund: ein Absturz darf das PDH nicht
	// beenden.
	defer func() {
		p := recover()
		if !started && p == nil {
			return
		}
		now := h.esppdhClock()
		u.mu.Lock()
		u.lastRun = now
		u.mu.Unlock()
		if p == nil {
			return
		}
		lg.Error().Interface("panic", p).Msg("esphome-prüfung abgestürzt")
		msg, err = "Interner Fehler bei der Prüfung – Näheres steht im Server-Protokoll.", fmt.Errorf("esphome-prüfung abgestürzt: %v", p)
		if started {
			st.Result, st.CheckedAt = msg, now
			if serr := esppdh.SaveUpdateState(st); serr != nil {
				lg.Error().Err(serr).Msg("zustand der esphome-prüfung speichern")
			}
		}
	}()

	if msg := h.esppdhUpdateBlocked(ctx, force); msg != "" {
		return msg, nil
	}
	st = esppdh.LoadUpdateState()
	started = true
	res, stopped := h.esppdhUpdateCheck(ctx, &st, force)
	if stopped {
		// Das PDH wird beendet: kein Vermerk „geprüft“ (weder Datei noch Speicher) – nach dem
		// Neustart wird bald wieder geprüft, und die Version gilt nicht als gescheitert.
		started = false
		lg.Info().Str("ergebnis", res).Msg("esphome-prüfung abgebrochen")
		return res, nil
	}
	st.Result = res
	st.CheckedAt = h.esppdhClock()
	if err := esppdh.SaveUpdateState(st); err != nil {
		lg.Error().Err(err).Msg("zustand der esphome-prüfung speichern")
		return st.Result, fmt.Errorf("Der Zustand der Prüfung ließ sich nicht speichern: %w", err)
	}
	lg.Info().Str("ergebnis", st.Result).Str("installiert", st.Installed).Bool("knopf", force).Msg("esphome geprüft")
	return st.Result, nil
}

// esppdhUpdateBlocked: warum jetzt nicht geprüft wird ("" = es geht).
func (h *Handler) esppdhUpdateBlocked(ctx context.Context, force bool) string {
	switch st := h.esppdhStateOf(ctx); {
	case st.Docker:
		return errEsppdhDocker.Error() + "."
	case !st.Enabled:
		return esppdhMsgOff
	case st.EnvMissing:
		return esppdhMsgNoEnv
	case !h.driveAgentConfigured():
		return "Der Update-Agent ist nicht eingerichtet – ohne ihn lässt sich ESPHome nicht aktualisieren."
	case !force && !h.esppdhAutoUpdate(ctx):
		return "„ESPHome selbst aktuell halten“ ist ausgeschaltet."
	}
	return ""
}

// esppdhUpdateCheck: Schritte 1–4; setzt Installed und die Felder zu gescheiterten Versionen,
// liefert das Ergebnis. stopped: Das PDH wird beendet – der Lauf zählt nicht als Prüfung.
func (h *Handler) esppdhUpdateCheck(ctx context.Context, st *esppdh.UpdateState, force bool) (result string, stopped bool) {
	// halt: ohne Urteil über die Version beendet; endete dabei ctx, wird das PDH beendet
	halt := func(note string) (string, bool) {
		if ctx.Err() != nil {
			return esppdhMsgStopped, true
		}
		return note, false
	}
	env := esppdhCheckEnv(ctx)
	st.Installed = env.Version
	if !env.Installed {
		if env.Problem != "" {
			return halt("Prüfung nicht möglich: " + env.Problem)
		}
		return halt("Prüfung nicht möglich: ESPHome ist an diesem Server nicht eingerichtet")
	}
	rels, err := h.esppdhReleases(ctx)
	if err != nil {
		return halt("Prüfung nicht möglich: " + err.Error())
	}
	var skip []string // alle unter dieser PDH-Version gescheiterten Versionen – „Jetzt prüfen“ versucht sie erneut
	if !force {
		skip = st.SkipFor(pdh.Version())
	}
	cand := esppdh.PickCandidate(rels, env.Version, esppdhPythonOf(esppdh.ActiveBin()), h.esppdhClock(), skip)
	if cand == "" {
		return "ESPHome " + env.Version + " ist aktuell", false
	}
	lg := componentLog("esppdh")
	lg.Info().Str("kandidat", cand).Str("installiert", env.Version).Msg("neue esphome-version gefunden")
	// Platz für die neue Umgebung: Fehlt der Versionen-Ordner noch, zählt sein nächster
	// vorhandener Elternordner (FreeMBAt); unbekannt (-1) hält nicht auf. Kein Urteil über die Version.
	if free := esppdhFreeMB(esppdh.VersionsDirPath()); free >= 0 && free < esppdhCandFreeMB {
		lg.Warn().Str("kandidat", cand).Int64("frei_mb", free).Msg("zu wenig platz für eine neue esphome-version")
		return esppdhMsgNoSpace, false
	}

	switch out, log, note := h.esppdhInstall(ctx, cand); out {
	case esppdhStopped:
		return halt(note)
	case esppdhFailed:
		return h.esppdhFailed(ctx, st, cand, env.Version, "ließ sich nicht einrichten", log), false
	}
	out, log, note := h.esppdhProbe(ctx, cand)
	switch out {
	case esppdhStopped:
		if ctx.Err() != nil { // den halben Kandidaten räumt der Agent vor der nächsten Einrichtung auf
			return esppdhMsgStopped, true
		}
		h.esppdhDiscard(ctx, cand)
		return note, false
	case esppdhFailed:
		return h.esppdhFailed(ctx, st, cand, env.Version, "hat die Probe nicht bestanden", log), false
	}
	actx, cancel := context.WithTimeout(ctx, esppdhSwitchTimeout)
	err = h.agentJSON(actx, http.MethodPost, "/v1/esppdh/versions", map[string]string{"action": "activate", "version": cand}, nil)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return esppdhMsgStopped, true
		}
		h.esppdhDiscard(ctx, cand)
		return "Umschalten auf ESPHome " + cand + " nicht möglich: " + err.Error() + " – es bleibt bei " + env.Version, false
	}
	st.Installed = cand
	st.Activated(cand) // gescheiterte und gemeldete Versionen bis cand sind erledigt
	u := &h.esppdhUpd
	u.mu.Lock()
	for v := range u.notified {
		if esppdh.CompareVersions(v, cand) <= 0 {
			delete(u.notified, v)
		}
	}
	u.mu.Unlock()
	return "Auf ESPHome " + cand + " umgestellt" + note, false
}

// esppdhFailed vermerkt eine gescheiterte Version (Kandidat verwerfen, Verlauf – schon gekürzt
// und geschwärzt) zu den schon gescheiterten und meldet sie einmal je Version an die
// ESPPDH-Bereichsadministratoren – nicht am Spiegel (Datenbank nur lesbar; der aktive Server
// meldet selbst).
func (h *Handler) esppdhFailed(ctx context.Context, st *esppdh.UpdateState, cand, old, what, log string) string {
	h.esppdhDiscard(ctx, cand)
	st.AddFailed(cand, pdh.Version(), log)
	if h.Mirror().On {
		return "ESPHome " + cand + " " + what + " – es bleibt bei " + old
	}
	// schon gemeldet – laut Zustandsdatei oder, falls sie sich nicht speichern ließ, im Speicher
	u := &h.esppdhUpd
	u.mu.Lock()
	notified := u.notified[cand]
	if u.notified == nil {
		u.notified = map[string]bool{}
	}
	u.notified[cand] = true
	u.mu.Unlock()
	if !st.WasNotified(cand) && !notified {
		h.systemNotify(ctx, h.usersWithPerm(ctx, permESPAdmin, ""),
			"ESPPDH: ESPHome "+cand+" "+what+". ESPPDH baut weiter mit ESPHome "+old+". Näheres unter Server-Einstellungen → ESPPDH.")
	}
	st.AddNotified(cand)
	return "ESPHome " + cand + " " + what + " – es bleibt bei " + old
}

// esppdhDiscard lässt den Agenten den Kandidaten löschen (bestmöglich: Fehler nur ins Protokoll –
// verwaiste Kandidaten räumt der Agent ohnehin vor jeder Einrichtung auf).
func (h *Handler) esppdhDiscard(ctx context.Context, cand string) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), esppdhSwitchTimeout)
	defer cancel()
	if err := h.agentJSON(actx, http.MethodPost, "/v1/esppdh/versions", map[string]string{"action": "discard", "version": cand}, nil); err != nil {
		componentLog("esppdh").Warn().Str("kandidat", cand).Str("fehler", err.Error()).Msg("kandidat verwerfen")
	}
}

// ── PyPI ─────────────────────────────────────────────────────

// esppdhReleases fragt PyPI nach den Ausgaben von ESPHome (Zeitlimit, höchstens 4 MB).
func (h *Handler) esppdhReleases(ctx context.Context) ([]esppdh.Release, error) {
	src := h.esppdhPyPIURL
	if src == "" {
		src = esppdhPyPIDefault
	}
	ctx, cancel := context.WithTimeout(ctx, esppdhPyPITimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pdh-server")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("PyPI antwortet nicht (Zeitlimit %s s)", strings.Replace(strconv.FormatFloat(esppdhPyPITimeout.Seconds(), 'f', -1, 64), ".", ",", 1))
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("PyPI ist nicht erreichbar (%v)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PyPI antwortet mit HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, esppdhPyPIMax+1))
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, errors.New("PyPI antwortet nicht vollständig (Zeitlimit)")
	case err != nil:
		return nil, fmt.Errorf("Die Antwort von PyPI ist unvollständig (%v)", err)
	case len(body) > esppdhPyPIMax:
		return nil, errors.New("Die Antwort von PyPI ist größer als 4 MB")
	}
	rels, err := esppdh.ParsePyPI(body)
	if err != nil {
		return nil, errors.New("Die Antwort von PyPI ist nicht lesbar")
	}
	if len(rels) == 0 {
		return nil, errors.New("PyPI nennt keine Ausgaben von ESPHome")
	}
	return rels, nil
}

// esppdhPythonOf: Python-Version der Umgebung des Programms bin (<venv>/pyvenv.cfg, „version =
// 3.12.3“); {0,0} = unbekannt – dann prüft PickCandidate die Python-Angabe nicht.
func esppdhPythonOf(bin string) [2]int {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(bin)), "pyvenv.cfg"))
	if err != nil {
		return [2]int{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || (strings.TrimSpace(k) != "version" && strings.TrimSpace(k) != "version_info") {
			continue
		}
		p := strings.SplitN(strings.TrimSpace(v), ".", 3)
		if len(p) < 2 {
			continue
		}
		maj, err1 := strconv.Atoi(p[0])
		min, err2 := strconv.Atoi(p[1])
		if err1 == nil && err2 == nil {
			return [2]int{maj, min}
		}
	}
	return [2]int{}
}

// ── Einrichten ───────────────────────────────────────────────

// esppdhInstall lässt den Agenten den Kandidaten einrichten und wartet auf das Ende (alle 5 s,
// höchstens 25 min). failed: die Einrichtung ist gescheitert (log = Meldung des Agenten);
// stopped: kein Urteil über die Version (note sagt, warum).
func (h *Handler) esppdhInstall(ctx context.Context, cand string) (out esppdhOutcome, log, note string) {
	notPossible := "Einrichtung von ESPHome " + cand + " nicht möglich: "
	// Kennt der Agent die ESPHome-Versionen nicht (vor 0.82.1), würde er die Grundumgebung einrichten.
	actx, cancel := context.WithTimeout(ctx, esppdhAgentTimeout)
	code, _, err := h.agentRaw(actx, http.MethodGet, "/v1/esppdh/versions", nil)
	cancel()
	switch {
	case err != nil:
		return esppdhStopped, "", notPossible + err.Error()
	case code == http.StatusNotFound:
		return esppdhStopped, "", esppdhMsgAgentOld
	}
	actx, cancel = context.WithTimeout(ctx, esppdhAgentTimeout)
	err = h.agentJSON(actx, http.MethodPost, "/v1/esppdh/install", map[string]string{"version": cand}, nil)
	cancel()
	if err != nil {
		return esppdhStopped, "", notPossible + err.Error()
	}
	limit := time.NewTimer(esppdhInstallLimit)
	defer limit.Stop()
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return esppdhStopped, "", esppdhMsgStopped
			case <-limit.C:
				return esppdhStopped, "", "Die Einrichtung von ESPHome " + cand + " dauert zu lange – neuer Versuch beim nächsten Lauf"
			case <-time.After(esppdhInstallPoll):
			}
		}
		var s esppdhJob
		actx, cancel := context.WithTimeout(ctx, esppdhAgentTimeout)
		err := h.agentJSON(actx, http.MethodGet, "/v1/esppdh/install", nil, &s)
		cancel()
		switch {
		case err != nil:
			return esppdhStopped, "", "Stand der Einrichtung von ESPHome " + cand + " nicht lesbar: " + err.Error()
		case s.Target != cand && s.Running:
			return esppdhStopped, "", "Am Server wird gerade eine andere ESPHome-Umgebung eingerichtet – neuer Versuch beim nächsten Lauf"
		case s.Target != cand:
			return esppdhStopped, "", "Die Einrichtung von ESPHome " + cand + " wurde unterbrochen – neuer Versuch beim nächsten Lauf"
		case s.Running || !s.Done:
			continue
		case s.Error != "":
			return esppdhFailed, esppdhTail(s.Error), ""
		case s.Version != cand:
			return esppdhFailed, "Der Update-Agent hat ESPHome " + s.Version + " statt " + cand + " eingerichtet.", ""
		}
		return esppdhPassed, "", ""
	}
}

// ── Probe ────────────────────────────────────────────────────

// esppdhProbed: ein Gerät, dessen Konfiguration mit dem Kandidaten durchging.
type esppdhProbed struct {
	d   esppdhDevice
	job esppdh.BuildJob
}

// esppdhProbe prüft den Kandidaten: Musterkonfigurationen, Geräte, am aktiven Server je
// Chip-Familie ein Probebau. passed: note nennt Übersprungenes (für das Ergebnis); failed: log
// ist das geschwärzte Ende der Ausgabe; stopped: note sagt, warum.
func (h *Handler) esppdhProbe(ctx context.Context, cand string) (out esppdhOutcome, log, note string) {
	bin := esppdh.VersionBin(cand)
	if why := esppdhProbeAbort(ctx, cand); why != "" {
		return esppdhStopped, "", why
	}
	run := h.esppdhProberFor(bin)
	// fail: an der Version gescheitert – außer, das PDH wird gerade beendet oder der Kandidat ist weg
	fail := func(what, log string, job esppdh.BuildJob) (esppdhOutcome, string, string) {
		if esppdhStopping(ctx) {
			return esppdhStopped, "", esppdhMsgStopped
		}
		if why := esppdhProbeAbort(ctx, cand); why != "" {
			return esppdhStopped, "", why
		}
		return esppdhFailed, what + ":\n" + esppdhTail(esppdh.Redact(log, job.SecretValues)), ""
	}
	// busy: Die Bau-Schlange blieb zu lange belegt (vertagt), oder das PDH wird beendet.
	busy := func() (esppdhOutcome, string, string) {
		if ctx.Err() != nil {
			return esppdhStopped, "", esppdhMsgStopped
		}
		return esppdhStopped, "", esppdhMsgVertagt
	}
	var notes esppdhProbeNotes

	for _, s := range esppdh.Samples() {
		job, err := esppdhProbeJob(s.Device)
		if err != nil {
			componentLog("esppdh").Error().Err(err).Str("muster", s.Name).Msg("musterkonfiguration erzeugen")
			return esppdhStopped, "", "Probe nicht möglich: Die Musterkonfiguration „" + s.Name + "“ lässt sich nicht erzeugen"
		}
		log, err := h.esppdhConfigLocked(ctx, run, job)
		switch {
		case errors.Is(err, errEsppdhQueueBusy):
			return busy()
		case err != nil:
			return fail("Musterkonfiguration „"+s.Name+"“, Prüfung mit ESPHome "+cand, log, job)
		}
	}

	devs, err := h.esppdhList(ctx)
	if err != nil {
		componentLog("esppdh").Error().Err(err).Msg("geräte für die probe lesen")
		return esppdhStopped, "", "Probe nicht möglich: Die Geräte ließen sich nicht lesen – neuer Versuch beim nächsten Lauf"
	}
	var probed []esppdhProbed
	for _, d := range devs {
		if d.Locked {
			continue
		}
		dev, err := h.esppdhBuildInput(ctx, d.ID, "")
		if err != nil {
			continue // lässt sich nicht auflösen – dann wird das Gerät auch nicht gebaut
		}
		job, err := esppdhProbeJob(dev)
		if err != nil {
			continue
		}
		log, err := h.esppdhConfigLocked(ctx, run, job)
		switch {
		case err == nil:
			probed = append(probed, esppdhProbed{d, job})
			continue
		case errors.Is(err, errEsppdhQueueBusy):
			return busy()
		}
		if esppdhStopping(ctx) { // ESPHome kann vor dem Abbruch beendet worden sein
			return esppdhStopped, "", esppdhMsgStopped
		}
		if why := esppdhProbeAbort(ctx, cand); why != "" {
			return esppdhStopped, "", why
		}
		// schon mit der laufenden Version kaputt? Dann zählt das Gerät nicht.
		old := job
		old.ID = uuid.NewString()
		_, oerr := h.esppdhConfigLocked(ctx, h.esppdhProberFor(esppdh.ActiveBin()), old)
		switch {
		case errors.Is(oerr, errEsppdhQueueBusy):
			return busy()
		case oerr != nil && ctx.Err() != nil:
			return esppdhStopped, "", esppdhMsgStopped
		case oerr != nil:
			notes.broken = append(notes.broken, d.Name)
			continue
		}
		return fail("Gerät „"+d.Name+"“ ("+d.Slug+"), Prüfung der Konfiguration mit ESPHome "+cand, log, job)
	}

	if h.Mirror().On { // gebaut wird nur am aktiven Server
		return esppdhPassed, "", notes.text()
	}
	builtOK, err := h.esppdhBuiltOK(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return esppdhStopped, "", esppdhMsgStopped
		}
		componentLog("esppdh").Error().Err(err).Msg("bauten für die probe lesen")
		return esppdhStopped, "", "Probe nicht möglich: Die Bauten der Geräte ließen sich nicht lesen – neuer Versuch beim nächsten Lauf"
	}
	for _, f := range esppdhProbeFamilies(probed, builtOK) {
		if f.p == nil {
			notes.noBuild = append(notes.noBuild, f.chip)
			continue
		}
		res, why := h.esppdhProbeBuild(ctx, run, f.p.job)
		if why != "" {
			return esppdhStopped, "", why
		}
		if res.OK { // die Firmware eines gelungenen Probebaus wird nicht gebraucht
			continue
		}
		if esppdhStopping(ctx) {
			return esppdhStopped, "", esppdhMsgStopped
		}
		// Baut das Gerät mit der laufenden Version? Sonst zählt der Fehlschlag nicht gegen die neue
		// (z. B. das Gerät lässt sich heute gar nicht mehr übersetzen).
		ores, why := h.esppdhProbeBuild(ctx, h.esppdhProberFor(esppdh.ActiveBin()), f.p.job)
		switch {
		case why != "":
			return esppdhStopped, "", why
		case !ores.OK:
			notes.buildBroken = append(notes.buildBroken, f.p.d.Name)
			continue
		}
		return fail("Gerät „"+f.p.d.Name+"“ ("+f.p.d.Slug+", Chip "+f.chip+"), Probebau mit ESPHome "+cand, res.Log, f.p.job)
	}
	return esppdhPassed, "", notes.text()
}

// esppdhProbeFamily: eine Chip-Familie unter den geprüften Geräten und das Gerät für ihren
// Probebau (nil: keines hat bisher erfolgreich gebaut – kein Probebau).
type esppdhProbeFamily struct {
	chip string
	p    *esppdhProbed
}

// esppdhProbeFamilies: je Chip-Familie (in der Reihenfolge der Geräte) das erste geprüfte Gerät,
// dessen letzter echter Bau gelang. So scheitert der Probebau nicht an einem Gerät, das sich
// schon mit der laufenden Version nicht bauen lässt.
func esppdhProbeFamilies(probed []esppdhProbed, builtOK map[string]bool) []esppdhProbeFamily {
	var out []esppdhProbeFamily
	at := map[string]int{}
	for i := range probed {
		p := &probed[i]
		chip := esppdh.ChipOf(p.d.BoardKey, p.d.CustomBoard)
		k, seen := at[chip]
		if !seen {
			k = len(out)
			at[chip] = k
			out = append(out, esppdhProbeFamily{chip: chip})
		}
		if out[k].p == nil && builtOK[p.d.ID] {
			out[k].p = p
		}
	}
	return out
}

// esppdhBuiltOK: Geräte, deren letzter echter Bau (gelungen oder gescheitert, nicht abgebrochen)
// gelang.
func (h *Handler) esppdhBuiltOK(ctx context.Context) (map[string]bool, error) {
	ok := map[string]bool{}
	if h.db == nil {
		return ok, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := h.db.Query(ctx, `SELECT device_id::text FROM (
			SELECT DISTINCT ON (device_id) device_id, status FROM esppdh_builds
			WHERE status IN ('ok', 'failed') AND finished_at IS NOT NULL
			ORDER BY device_id, finished_at DESC, id DESC) b
		WHERE status = 'ok'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ok[id] = true
	}
	return ok, rows.Err()
}

// esppdhProbeBuild: ein Probebau mit run. Wartet, bis die Bau-Schlange frei ist, hält dann runMu
// und prüft vorher den Platz im Arbeitsordner. why != "": nicht gebaut (Schlange zu lange belegt,
// zu wenig Platz, PDH beendet) – kein Urteil über die Version.
func (h *Handler) esppdhProbeBuild(ctx context.Context, run esppdhProbeRunner, job esppdh.BuildJob) (res esppdh.BuildResult, why string) {
	if !h.esppdhProbeLock(ctx) {
		if ctx.Err() != nil {
			return res, esppdhMsgStopped
		}
		return res, esppdhMsgVertagt
	}
	defer h.esppdhQ.runMu.Unlock()
	if free := esppdhFreeMB(esppdh.WorkDirPath()); free >= 0 && free < esppdh.BuildMinFreeMB {
		return res, fmt.Sprintf("Probebau nicht möglich: Zu wenig Platz für den Bau (frei %d MB, nötig %d MB) – neuer Versuch beim nächsten Lauf",
			free, esppdh.BuildMinFreeMB)
	}
	job.ID = uuid.NewString()
	return run.Build(ctx, job, nil), ""
}

// esppdhProbeAbort: Grund, die Probe ohne Urteil über die Version abzubrechen ("" = keiner).
// Der Agent löscht Kandidaten, die nicht zum laufenden Auftrag gehören, vor jeder Einrichtung.
func esppdhProbeAbort(ctx context.Context, cand string) string {
	if ctx.Err() != nil {
		return esppdhMsgStopped
	}
	if _, err := os.Stat(esppdh.VersionBin(cand)); err != nil {
		return "Kandidat ESPHome " + cand + " nicht mehr vorhanden – neuer Versuch beim nächsten Lauf"
	}
	return ""
}

// esppdhProbeJob: Prüfauftrag für ein Gerät (eigene Kennung, Kennwörter zum Schwärzen).
func esppdhProbeJob(d esppdh.Device) (esppdh.BuildJob, error) {
	cfg, sec, err := esppdh.Generate(d)
	if err != nil {
		return esppdh.BuildJob{}, err
	}
	return esppdh.BuildJob{ID: uuid.NewString(), Slug: d.Slug, Config: cfg, Secrets: sec, SecretValues: d.SecretValues()}, nil
}

// esppdhConfigLocked: „esphome config“ unter runMu – nie gleichzeitig mit einem Bau. Am aktiven
// Server wartet jede Prüfung wie der Probebau, bis kein Bau wartet oder läuft: Sonst käme ein
// eingereihter Bau erst nach allen Prüfungen dran. errEsppdhQueueBusy: zu lange gewartet oder ctx
// beendet. Am Spiegel wird nicht gebaut (die Tabelle zeigt dort die Schlange des aktiven Servers).
func (h *Handler) esppdhConfigLocked(ctx context.Context, run esppdhProbeRunner, job esppdh.BuildJob) (string, error) {
	if h.Mirror().On {
		h.esppdhQ.runMu.Lock()
	} else if !h.esppdhProbeLock(ctx) {
		return "", errEsppdhQueueBusy
	}
	defer h.esppdhQ.runMu.Unlock()
	return run.Config(ctx, job)
}

// esppdhProbeLock wartet, bis kein Bau wartet oder läuft, und hält dann runMu (der Probebau
// läuft hinter echten Aufträgen). false: nach esppdhIdleLimit noch belegt oder ctx beendet.
func (h *Handler) esppdhProbeLock(ctx context.Context) bool {
	limit := time.NewTimer(esppdhIdleLimit)
	defer limit.Stop()
	for {
		if h.esppdhQ.runMu.TryLock() {
			if h.esppdhQueueIdle(ctx) {
				return true
			}
			h.esppdhQ.runMu.Unlock()
		}
		select {
		case <-ctx.Done():
			return false
		case <-limit.C:
			return false
		case <-time.After(esppdhIdleWait):
		}
	}
}

// esppdhProbeNotes: was die Probe übersprungen hat, ohne dass es gegen die neue Version zählt.
type esppdhProbeNotes struct {
	broken      []string // Geräte, deren Konfiguration schon mit der bisherigen Version scheitert
	noBuild     []string // Chip-Familien ohne Gerät mit gelungenem Bau – kein Probebau
	buildBroken []string // Geräte, deren Probebau auch mit der bisherigen Version scheitert
}

// text: Zusatz zum Ergebnis ("" = nichts übersprungen).
func (n esppdhProbeNotes) text() string {
	var parts []string
	if len(n.broken) > 0 {
		parts = append(parts, "übersprungen, weil schon mit der bisherigen Version fehlerhaft: "+esppdhNames(n.broken))
	}
	if len(n.noBuild) > 0 {
		parts = append(parts, "Probebau übersprungen für Chip "+strings.Join(n.noBuild, ", ")+": noch kein Gerät erfolgreich gebaut")
	}
	if len(n.buildBroken) > 0 {
		parts = append(parts, "Probebau übersprungen, weil der Bau schon mit der bisherigen Version scheitert: "+esppdhNames(n.buildBroken))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// esppdhNames: höchstens esppdhSkipNames Namen, der Rest als „und N weitere“.
func esppdhNames(names []string) string {
	more := ""
	if len(names) > esppdhSkipNames {
		more = fmt.Sprintf(" und %d weitere", len(names)-esppdhSkipNames)
		names = names[:esppdhSkipNames]
	}
	return strings.Join(names, ", ") + more
}

// esppdhTail: die letzten 40 Zeilen (höchstens 16 KB) eines schon geschwärzten Verlaufs.
func esppdhTail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > esppdhFailLines {
		lines = lines[len(lines)-esppdhFailLines:]
	}
	s = strings.Join(lines, "\n")
	if len(s) > esppdhFailMax {
		i := len(s) - esppdhFailMax
		for i < len(s) && (s[i]&0xC0) == 0x80 { // nicht mitten in einem Zeichen beginnen
			i++
		}
		s = "…" + s[i:]
	}
	return s
}
