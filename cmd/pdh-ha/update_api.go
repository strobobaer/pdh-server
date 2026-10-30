package main

// Schnittstelle zum Update im Verbund (Spec 6): Auftrag starten, abbrechen,
// anzeigen, fehlerhafte Versionen verwalten, Wartungsfenster einstellen.
//
//	GET    /v1/update                  -> ha.UpdateView
//	POST   /v1/update/start            {"commit","source","kind","force","version"}
//	POST   /v1/update/abort            {"by"}
//	DELETE /v1/update/faulty/{commit}
//	PUT    /v1/window                  ha.Window
//
// Nicht grün: 409 {"ok":false,"error":<Grund>,"hints":[…]}.

import (
	"context"
	"errors"
	"flag"
	"io"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"pdh/internal/ha"
)

// Quellen eines Auftrags.
const (
	srcKnopf    = "knopf"
	srcZeitplan = "zeitplan"
)

type updateStartReq struct {
	Commit  string `json:"commit"`
	Source  string `json:"source"`  // "knopf" (Standard) | "zeitplan"
	Kind    string `json:"kind"`    // leer = Art aus dem Wartungsfenster
	Force   bool   `json:"force"`   // nur mit "knopf": trotzdem, mit kurzer Unterbrechung
	Version string `json:"version"` // Anzeigename der Zielversion; leer = Kurzform des Commits
}

// notReadyError: Prüfung „alles grün“ nicht bestanden (HTTP 409).
type notReadyError struct {
	reason string
	hints  []string
}

func (e notReadyError) Error() string { return e.reason }

// windowLocked: Wartungsfenster der Verbund-Einstellungen; ungültig oder nie
// gesetzt (ältere Konfiguration) = Standard.
func (d *daemon) windowLocked() ha.Window {
	if w := d.cl.Window; w.Validate() == nil {
		return w
	}
	return ha.DefaultWindow()
}

// readyObsLocked baut die Sicht für die Prüfung „alles grün“ aus dem Stand
// des Dienstes. Nicht bekannt sind dem Dienst der freie Platz (die
// Herzschlag-Angabe „disk“ ist nur ein Platzhalter) und der Stand der
// Sicherungen: beide werden hier nicht geprüft (BackupConfigured=false,
// Platz unbegrenzt); das Wartungsfenster (Task 7) prüft die Sicherungen selbst.
func (d *daemon) readyObsLocked(partner, witness string) ha.ReadyObs {
	s := d.settingsLocked()
	pv := d.views[partner]
	o := ha.ReadyObs{
		Mode: d.modeLocked(), SelfRole: d.p.Role,
		PartnerSeen: pv.seen(), PartnerRole: ha.Role(pv.Last.Role),
		SameTerm: pv.seen() && pv.Last.Term == d.p.Term,
		Maint:    d.p.Maint || (pv.seen() && pv.Last.Maint),
		Busy:     d.busy.Load(), JobOpen: d.up.job.ID != "" && !upEnded(d.up.job),
		SelfCommit: d.commit, PartnerCommit: pv.Last.Commit,
		MirrorSync: d.mirrorSyncLocked(s, partner, pv), LagLimit: s.LagLimit,
		FreeSelf: math.MaxInt64, FreePartner: math.MaxInt64,
		WitnessExpected: witness != "", WitnessSeen: witness != "" && d.views[witness].seen(),
	}
	return o
}

// startUpdate prüft und legt einen Auftrag an. Rückgabe: Hinweise, die nicht
// hindern (z. B. Zeuge nicht erreichbar). Ein nicht grüner Stand ist ein
// notReadyError.
func (d *daemon) startUpdate(req updateStartReq) ([]string, error) {
	d.lock()
	defer d.unlock()
	if err := d.clusterEditable(); err != nil {
		return nil, err
	}
	if d.modeLocked() == ha.Single {
		return nil, errors.New("Update im Verbund nur mit zwei Servern möglich")
	}
	if d.p.Role != ha.Active {
		return nil, errors.New("Update nur auf dem aktiven Server starten")
	}
	src := req.Source
	if src == "" {
		src = srcKnopf
	}
	if src != srcKnopf && src != srcZeitplan {
		return nil, fmt.Errorf("Unbekannte Quelle %q", req.Source)
	}
	if req.Force && src != srcKnopf {
		return nil, errors.New("Erzwingen ist nur über den Knopf möglich")
	}
	req.Commit = strings.TrimSpace(req.Commit)
	if req.Commit == "" {
		return nil, errors.New("Commit fehlt")
	}
	kind := ha.UpdateKind(req.Kind)
	switch kind {
	case "":
		kind = d.windowLocked().Kind
	case ha.UpdateRolling, ha.UpdateSequential:
	default:
		return nil, fmt.Errorf("Unbekannte Update-Art %q", req.Kind)
	}
	if req.Force {
		kind = ha.UpdateSequential // kurze Unterbrechung statt Rollentausch
	}
	if src == srcZeitplan && slices.Contains(d.up.faulty, req.Commit) {
		return nil, notReadyError{reason: "Version als fehlerhaft gemerkt"}
	}
	partner, witness := d.peerIDsLocked()
	if partner == "" {
		return nil, errors.New("Kein Partner eingerichtet")
	}
	var hints []string
	if cur := d.up.job; cur.ID != "" && !upEnded(cur) {
		return nil, errors.New("Ein Update-Auftrag läuft bereits")
	}
	if req.Force {
		// Der Knopf darf trotz nicht grün weitermachen, aber nicht ohne Partner:
		// sonst hinge der Auftrag an einem Server, der nicht antwortet.
		if !d.views[partner].seen() {
			return nil, errors.New("Partner nicht erreichbar")
		}
	} else {
		ok, reason, h := ha.UpdateReady(d.readyObsLocked(partner, witness))
		if !ok {
			return nil, notReadyError{reason: reason, hints: h}
		}
		hints = h
	}
	j := ha.UpdateJob{
		ID:   "u" + d.wall().UTC().Format("20060102T150405.000"),
		Kind: kind, Source: src, Target: req.Commit, TargetVersion: strings.TrimSpace(req.Version),
		OldVersion: d.version,
		Old:        map[string]string{d.node.ID: d.commit, partner: d.views[partner].Last.Commit},
		// rolling: der passive Partner zuerst; sequential: der aktive selbst zuerst
		First: partner, Second: d.node.ID,
	}
	if kind == ha.UpdateSequential {
		j.First, j.Second = d.node.ID, partner
	}
	if err := d.startUpdateLocked(j); err != nil {
		return nil, err
	}
	d.event("info", fmt.Sprintf("Update auf %s angelegt (%s, %s)", shortCommit(req.Commit), kind, src))
	return hints, nil
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// updateView: Auftrag für GET /v1/update.
func (d *daemon) updateView() ha.UpdateView {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, lockMax, _ := d.updateDurationsLocked()
	return d.up.job.View(d.wall(), lockMax, d.up.faulty)
}

// allowedEditor: gleiche Prüfung wie bei den Einstellungen – CA-Knoten oder
// der aktive Server (mit Spiegel läuft das PDH nur am aktiven).
func (d *daemon) allowedEditorLocked() error {
	if !d.node.CAHolder && d.p.Role != ha.Active {
		return errors.New("Einstellungen bitte am aktiven Server ändern")
	}
	return nil
}

// forgetFaulty nimmt einen Commit aus der Liste der fehlerhaften Versionen.
func (d *daemon) forgetFaulty(commit string) error {
	d.lock()
	defer d.unlock()
	if err := d.clusterEditable(); err != nil {
		return err
	}
	if err := d.allowedEditorLocked(); err != nil {
		return err
	}
	if !slices.Contains(d.up.faulty, commit) {
		return errors.New("Version ist nicht als fehlerhaft gemerkt")
	}
	f := slices.DeleteFunc(slices.Clone(d.up.faulty), func(c string) bool { return c == commit })
	if err := d.st.saveFaulty(f); err != nil {
		return fmt.Errorf("Liste nicht gespeichert: %w", err)
	}
	d.up.faulty = f
	d.event("info", "Version "+shortCommit(commit)+" nicht mehr als fehlerhaft gemerkt")
	return nil
}

// updateWindow ändert das Wartungsfenster für den ganzen Verbund.
func (d *daemon) updateWindow(w ha.Window) error {
	if err := w.Validate(); err != nil {
		return err
	}
	d.lock()
	if err := d.clusterEditable(); err != nil {
		d.unlock()
		return err
	}
	if err := d.allowedEditorLocked(); err != nil {
		d.unlock()
		return err
	}
	cl := d.cl
	cl.Window = w
	cl.Version++
	pl, err := d.publishClusterLocked(cl)
	d.unlock()
	if err != nil {
		return err
	}
	pl.event("info", "Wartungsfenster geändert")
	d.execute(context.Background(), pl)
	return nil
}

func registerUpdateRoutes(mux *http.ServeMux, d *daemon) {
	mux.HandleFunc("GET /v1/update", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, d.updateView())
	})
	mux.HandleFunc("POST /v1/update/start", func(w http.ResponseWriter, r *http.Request) {
		var req updateStartReq
		if !decodeBody(w, r, &req) {
			return
		}
		hints, err := d.startUpdate(req)
		var nr notReadyError
		switch {
		case errors.As(err, &nr):
			h := nr.hints
			if h == nil {
				h = []string{}
			}
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": nr.reason, "hints": h})
		case err != nil:
			apiFail(w, err)
		default:
			if hints == nil {
				hints = []string{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hints": hints})
		}
	})
	mux.HandleFunc("POST /v1/update/abort", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			By string `json:"by"`
		}
		if !decodeBody(w, r, &req) {
			return
		}
		if err := d.abortUpdate(req.By); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	mux.HandleFunc("DELETE /v1/update/faulty/{commit}", func(w http.ResponseWriter, r *http.Request) {
		if err := d.forgetFaulty(r.PathValue("commit")); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
	mux.HandleFunc("PUT /v1/window", func(w http.ResponseWriter, r *http.Request) {
		var win ha.Window
		if !decodeBody(w, r, &win) {
			return
		}
		if err := d.updateWindow(win); err != nil {
			apiFail(w, err)
			return
		}
		apiOK(w)
	})
}

// printUpdate zeigt den Auftrag für „pdh-ha update status“.
func printUpdate(w io.Writer, v ha.UpdateView) {
	if !v.Active && v.Kind == "" {
		fmt.Fprintln(w, "Update: kein Auftrag")
	} else {
		state := "läuft"
		if !v.Active {
			state = "beendet"
		}
		fmt.Fprintf(w, "Update: %s (%s)\n", state, v.Kind)
		fmt.Fprintf(w, "Schritt: %s – %s (%d %%)\n", v.Phase, v.Text, v.Percent)
		if v.FromVersion != "" || v.ToVersion != "" {
			fmt.Fprintf(w, "Version: %s -> %s\n", v.FromVersion, v.ToVersion)
		}
		if v.Locked {
			fmt.Fprintf(w, "Gesperrt, noch höchstens %d s\n", v.RemainingS)
		}
		if !v.CountdownUntil.IsZero() && v.Phase == string(ha.UpCountdown) {
			fmt.Fprintf(w, "Sperre um %s\n", v.CountdownUntil.Local().Format(time.TimeOnly))
		}
		if v.Result != "" {
			fmt.Fprintf(w, "Ergebnis: %s\n", v.Result)
		}
	}
	if len(v.Faulty) > 0 {
		fmt.Fprintf(w, "Fehlerhafte Versionen: %s\n", strings.Join(v.Faulty, ", "))
	}
}

// cmdUpdate: „pdh-ha update status|start|abort“.
func cmdUpdate(args []string, sock string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, "pdh-ha:", err)
		return 1
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "status":
		var v ha.UpdateView
		if err := apiDo(sock, http.MethodGet, "/v1/update", nil, &v); err != nil {
			return fail(err)
		}
		printUpdate(stdout, v)
		return 0
	case "abort":
		if err := apiDo(sock, http.MethodPost, "/v1/update/abort", map[string]string{"by": "Befehlszeile"}, nil); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "Abbruch vorgemerkt – der Auftrag wird zurückgesetzt.")
		return 0
	case "start":
		fs := flag.NewFlagSet("pdh-ha update start", flag.ContinueOnError)
		fs.SetOutput(stderr)
		kind := fs.String("kind", "", "rolling (ohne Unterbrechung) oder sequential (alle nacheinander); Standard: Einstellung des Fensters")
		version := fs.String("version", "", "Anzeigename der Zielversion")
		// Commit steht vor den Schaltern: „update start <commit> --kind …“
		rest := args[1:]
		commit := ""
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			commit, rest = rest[0], rest[1:]
		}
		if fs.Parse(rest) != nil {
			return 2
		}
		if commit == "" && fs.NArg() == 1 {
			commit = fs.Arg(0)
		} else if fs.NArg() != 0 {
			commit = ""
		}
		if commit == "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		var res struct {
			Hints []string `json:"hints"`
		}
		err := apiDo(sock, http.MethodPost, "/v1/update/start",
			map[string]any{"commit": commit, "source": srcKnopf, "kind": *kind, "version": *version}, &res)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "Update-Auftrag angelegt.")
		for _, h := range res.Hints {
			fmt.Fprintln(stdout, "Hinweis:", h)
		}
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}
