// pdh-ha ist der Ausfallschutz-Dienst für zwei PDH-Server und einen Zeugen.
// Automatische Umschaltung standardmäßig nur beobachtet (auto_mode observe).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// per -ldflags "-X main.version=… -X main.commit=…"
var (
	version = "dev"
	commit  = ""
)

const usage = `Aufruf: pdh-ha <Befehl>
  run [--version-override V]          Dienst starten
  setup [--addr a,b] [--id K]         diesen Server als ersten einrichten
  join [--witness] [--addr a,b] [--id K] [<Code>]
                                      einem Cluster beitreten (Code sonst aus PDH_HA_JOIN)
  join-code [--witness]               Beitrittscode erzeugen
  status [--json]                     Zustand anzeigen
  maintenance on|off                  Wartung (Automatik aus/an)
  settings '<json>'                   Zeiten setzen ({"settings":{…},"gateway_targets":[…]})
  access [--vip ip/präfix] [--vip-on|--vip-off] [--hooks-on|--hooks-off]
                                      Zugangsweg: mitwandernde Adresse und Skripte
                                      (--vip-on erst nach erfolgreichem vip test)
  vip test [--adresse ip/präfix]      mitwandernde Adresse in alle Richtungen testen
                                      (Standard: die eingetragene Adresse)
  reset --yes                         nach dem Entfernen: Konfiguration sichern und
                                      zurücksetzen (danach setup oder join)
  replica-init [--confirm-host NAME]  diesen (passiven) Server als Datenbank-Spiegel
                                      des aktiven einrichten (überschreibt die Datenbank)
  peer-pg-major                       PostgreSQL-Hauptversion des aktiven Servers
  should-run-pdh                      Exit 0, wenn pdh hier laufen soll
  version                             Version anzeigen
`

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "pdh-ha:", err)
		return 1
	}
	fs := flag.NewFlagSet("pdh-ha "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	sock := socketPath()
	switch args[0] {
	case "run":
		override := fs.String("version-override", "", "Version vortäuschen (nur Testlabor)")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		if err := cmdRun(*override, stderr); err != nil {
			return fail(err)
		}
		return 0
	case "setup":
		addr := fs.String("addr", "", "eigene Adressen, Komma-getrennt (Port 7480 wird ergänzt)")
		id := fs.String("id", "", "Kennung (Standard: Hostname)")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		o := setupOpts{ID: *id, Addrs: splitList(*addr)}
		err := apiDo(sock, http.MethodPost, "/v1/setup", map[string]any{"addrs": o.Addrs, "id": o.ID}, nil)
		var ae apiError
		if err != nil && !errors.As(err, &ae) {
			// Dienst läuft nicht: direkt in die Ablage, der Dienst übernimmt beim Start
			err = setupFiles(newStore(), o)
		}
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "Ausfallschutz eingerichtet (Wartung an).")
		return 0
	case "join":
		witness := fs.Bool("witness", false, "als Zeuge beitreten")
		addr := fs.String("addr", "", "eigene Adressen, Komma-getrennt (Port 7480 wird ergänzt)")
		id := fs.String("id", "", "Kennung (Standard: Hostname)")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		// Code als Argument oder aus PDH_HA_JOIN (taucht dann nicht in ps auf)
		code := strings.TrimSpace(os.Getenv("PDH_HA_JOIN"))
		if fs.NArg() == 1 {
			code = fs.Arg(0)
		}
		if fs.NArg() > 1 || code == "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		kind := "node"
		if *witness {
			kind = "witness"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		n, err := joinFiles(ctx, newStore(), code, joinOpts{ID: *id, Kind: kind, Addrs: splitList(*addr)})
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Beigetreten als %s (%s)\n", n.ID, kindText(n.Kind))
		return 0
	case "join-code":
		witness := fs.Bool("witness", false, "Code für einen Zeugen")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		role := "node"
		if *witness {
			role = "witness"
		}
		var r struct {
			Code    string    `json:"code"`
			Expires time.Time `json:"expires"`
		}
		if err := apiDo(sock, http.MethodPost, "/v1/join-code", map[string]string{"role": role}, &r); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, r.Code)
		fmt.Fprintln(stderr, "gültig bis", r.Expires.Local().Format("15:04"))
		return 0
	case "status":
		asJSON := fs.Bool("json", false, "als JSON")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		var raw json.RawMessage
		if err := apiDo(sock, http.MethodGet, "/v1/status", nil, &raw); err != nil {
			return fail(err)
		}
		if *asJSON {
			fmt.Fprintln(stdout, string(raw))
			return 0
		}
		var st apiStatus
		if err := json.Unmarshal(raw, &st); err != nil {
			return fail(err)
		}
		printStatus(stdout, st)
		return 0
	case "maintenance":
		if len(args) != 2 || args[1] != "on" && args[1] != "off" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		if err := apiDo(sock, http.MethodPost, "/v1/maintenance", map[string]bool{"on": args[1] == "on"}, nil); err != nil {
			return fail(err)
		}
		return 0
	case "settings":
		if len(args) != 2 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		if err := apiDo(sock, http.MethodPut, "/v1/settings", args[1], nil); err != nil {
			return fail(err)
		}
		return 0
	case "access":
		vip := fs.String("vip", "", "mitwandernde Adresse mit Präfix, z. B. 10.10.0.202/24 (leer = entfernen)")
		vipOn := fs.Bool("vip-on", false, "mitwandernde Adresse einschalten")
		vipOff := fs.Bool("vip-off", false, "mitwandernde Adresse ausschalten")
		hooksOn := fs.Bool("hooks-on", false, "Skripte beim Rollenwechsel einschalten")
		hooksOff := fs.Bool("hooks-off", false, "Skripte beim Rollenwechsel ausschalten")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
			return 2
		}
		body := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "vip" {
				body["vip"] = *vip
			}
		})
		if *vipOn || *vipOff {
			body["vip_on"] = *vipOn
		}
		if *hooksOn || *hooksOff {
			body["hooks_on"] = *hooksOn
		}
		if *vipOn && *vipOff || *hooksOn && *hooksOff || len(body) == 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		if err := apiDo(sock, http.MethodPut, "/v1/access", body, nil); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "Zugangsweg gespeichert.")
		return 0
	case "vip":
		if len(args) < 2 || args[1] != "test" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		addr := fs.String("adresse", "", "Prüfadresse mit Präfix (Standard: die mitwandernde Adresse)")
		if fs.Parse(args[2:]) != nil || fs.NArg() != 0 {
			return 2
		}
		var r struct {
			Results []vipCheckResult `json:"results"`
		}
		if err := apiDoT(sock, 3*time.Minute, http.MethodPost, "/v1/vip/test", map[string]string{"addr": *addr}, &r); err != nil {
			return fail(err)
		}
		if printVIPCheck(stdout, r.Results) {
			return 1
		}
		fmt.Fprintln(stdout, "Test erfolgreich – die mitwandernde Adresse kann eingeschaltet werden.")
		return 0
	case "reset":
		yes := fs.Bool("yes", false, "bestätigen")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		if !*yes {
			return fail(errors.New("Zum Bestätigen --yes angeben"))
		}
		st := newStore()
		backup, err := resetFiles(st, time.Now())
		if err != nil {
			return fail(err)
		}
		if backup == "" {
			fmt.Fprintln(stdout, "Nichts zurückzusetzen.")
		} else {
			fmt.Fprintf(stdout, "Zurückgesetzt. Sicherung: %s (Zustand: %s.reset-…)\n", backup, st.state)
		}
		return 0
	case "version":
		fmt.Fprintf(stdout, "pdh-ha %s %s\n", version, commit)
		return 0
	case "should-run-pdh":
		if shouldRunPDH(newStore(), localPGState) {
			return 0
		}
		return 1
	case "peer-pg-major":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		info, _, err := pullFromActive(ctx, newStore(), true)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, info.PGMajor)
		return 0
	case "replica-init":
		confirm := fs.String("confirm-host", "", "Hostname zur Bestätigung, wenn hier schon Datenbanken liegen")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		if err := cmdReplicaInit(*confirm); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "Datenbank-Spiegel läuft – im PDH Rollen tauschen (hin und zurück) testen.")
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// localPGState: Zustand des PostgreSQL hier (pgCtl.status); nicht lesbar →
// "error" (gilt nicht als Standby).
func localPGState() string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := execRunner{}
	c, err := discoverCluster(ctx, r)
	if err != nil {
		return "error"
	}
	return (&pgCtl{r: r, c: c}).status(ctx).State
}

// cmdReplicaInit baut den pgActor für diesen Server und richtet den Spiegel ein.
func cmdReplicaInit(confirm string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	st := newStore()
	n, err := st.loadNode()
	if err != nil {
		return err
	}
	cl, err := st.loadCluster()
	if err != nil {
		return err
	}
	r := execRunner{}
	c, err := discoverCluster(ctx, r)
	if err != nil {
		return err
	}
	self := clusterNode{ID: n.ID, Kind: n.Kind, Addrs: n.Addrs}
	if sn, ok := findNode(cl, n.ID); ok {
		self = sn
	}
	ev := openEventLog(st.statePath("replica-init.jsonl"), 1<<20, 2)
	pa := newPGActor(&pgCtl{r: r, c: c}, newServiceCtl(r), newTunnelCtl(r), ev, func() pgEnv { return buildPGEnv(self, clusterNode{}, cl, "", c, true) })
	return replicaInit(ctx, st, pa, replicaOpts{
		ConfirmHost: confirm, Repo: pdhRepo(), Etc: pdhEtc(), Hostname: os.Hostname, Wait: 2 * time.Minute,
		Pull: func(ctx context.Context, dry bool) (pullResp, clusterNode, error) {
			return pullFromActive(ctx, st, dry)
		},
	})
}

func cmdRun(override string, stderr io.Writer) error {
	st := newStore()
	if err := st.fixPerms(); err != nil {
		fmt.Fprintln(stderr, "pdh-ha: Rechte von", st.etc, "nicht gesetzt:", err)
	}
	d := newDaemon(st, openEventLog(st.statePath("events.jsonl"), 5<<20, 3))
	if override != "" {
		d.version, d.versionOverride = override, true
	}
	d.hostID = hostID()
	if err := d.load(); err != nil {
		// weiterlaufen: die API meldet den Fehler, der Takt lädt erneut
		fmt.Fprintln(stderr, "pdh-ha: Konfiguration nicht lesbar:", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ready := make(chan error, 1)
	apiDone := make(chan error, 1)
	go func() { apiDone <- serveAPI(ctx, d, socketPath(), ready) }()
	if err := <-ready; err != nil {
		return fmt.Errorf("Socket %s: %w", socketPath(), err)
	}
	d.event("info", fmt.Sprintf("pdh-ha %s gestartet", d.version))
	go func() {
		if err := <-apiDone; err != nil {
			fmt.Fprintln(stderr, "pdh-ha: Socket:", err)
			stop()
		}
	}()
	return d.run(ctx)
}

// printVIPCheck druckt je Richtung eine Zeile; true = mindestens ein Fehler.
func printVIPCheck(w io.Writer, res []vipCheckResult) bool {
	bad := len(res) == 0
	for _, r := range res {
		switch {
		case r.Checker == "":
			fmt.Fprintf(w, "%s hält: Fehler – %s\n", r.Holder, r.Err)
		case r.OK:
			fmt.Fprintf(w, "%s hält, %s prüft: ok\n", r.Holder, r.Checker)
		default:
			fmt.Fprintf(w, "%s hält, %s prüft: Fehler – %s\n", r.Holder, r.Checker, r.Err)
		}
		bad = bad || !r.OK
	}
	return bad
}

func printStatus(w io.Writer, st apiStatus) {
	if !st.Configured {
		fmt.Fprintln(w, "Betriebsart: einzel (Ausfallschutz nicht eingerichtet)")
		return
	}
	auto := "echt"
	if st.Observe {
		auto = "nur Beobachtung"
	}
	fmt.Fprintf(w, "Betriebsart: %s (automatische Umschaltung: %s)\n", st.Mode, auto)
	fmt.Fprintf(w, "Dieser Server: %s (%s) – %s, Amtszeit %d, Version %s\n", st.Self.ID, kindText(st.Self.Kind), st.Self.Role, st.Self.Term, st.Self.Version)
	maint := "aus"
	if st.Maint {
		maint = "an (Automatik aus)"
	}
	fmt.Fprintf(w, "Wartung: %s\n", maint)
	if st.Blocked != "" {
		fmt.Fprintf(w, "Übernahme gesperrt: %s\n", st.Blocked)
	}
	if a := st.Access; a.VIP != "" {
		addr, _, _ := strings.Cut(a.VIP, "/")
		where := "niemand"
		switch a.Holder {
		case "":
		case st.Self.ID:
			where = "hier"
		default:
			where = "bei " + a.Holder
		}
		if !a.VIPOn {
			where += " (aus)"
		}
		fmt.Fprintf(w, "Adresse %s: %s\n", addr, where)
		if a.IfaceErr != "" {
			fmt.Fprintf(w, "  Netzwerkkarte: %s\n", a.IfaceErr)
		}
	}
	for _, p := range st.Peers {
		seen := "nicht gesehen"
		if p.Seen {
			seen = fmt.Sprintf("gesehen vor %.1f s", p.LastSeenAgo)
		} else if p.LastSeenAgo >= 0 {
			seen = fmt.Sprintf("zuletzt vor %.0f s", p.LastSeenAgo)
		}
		fmt.Fprintf(w, "  %s (%s): %s, Amtszeit %d, Version %s, %s\n", p.ID, kindText(p.Kind), p.Role, p.Term, p.Version, seen)
		for _, pa := range p.Paths {
			state := "weg"
			if pa.Alive {
				state = "ok"
			}
			if pa.Err != "" {
				state += " (" + pa.Err + ")"
			}
			fmt.Fprintf(w, "      Weg %s: %s\n", pa.Addr, state)
		}
	}
	for _, x := range st.Warnings {
		fmt.Fprintf(w, "Hinweis: %s\n", x)
	}
}
