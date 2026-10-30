package main

// Aufruf des lokalen Update-Agenten (Update im Verbund, Spec 3.2) und
// Abfrage des eigenen pdh (/health). Token, Adresse und Port stehen in der
// .env des PDH (/opt/pdh-server/.env, Pfad über PDH_HA_PDH_ENV); im Labor
// ersetzt ein Befehl (PDH_HA_AGENT_CMD) den Agenten.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// agentClient spielt einen Stand ein, dreht zurück oder setzt einen
// vorbereiteten Bau ein. Jeder Aufruf wartet auf das Ergebnis.
type agentClient interface {
	Install(ctx context.Context, commit string, noRestart bool) error
	Rollback(ctx context.Context, commit string, noRestart bool) error
	Finish(ctx context.Context) error
	// Busy: läuft beim Agenten gerade ein Lauf (z. B. ein Einspielen, das
	// pdh-ha am Ende neu startet)? Fehler = unbekannt.
	Busy(ctx context.Context) (bool, error)
}

// agentWait: so lange wartet ein Aufruf höchstens auf das Ende des Updates.
const agentWait = 20 * time.Minute

// agentRestartGrace: so lange gilt ein nicht erreichbarer Agent als „startet gerade neu“.
const agentRestartGrace = time.Minute

// newAgentClient: PDH_HA_AGENT_CMD gesetzt (Labor) → Befehl; sonst HTTP an
// den Update-Agenten.
func newAgentClient() agentClient {
	if c := strings.Fields(os.Getenv("PDH_HA_AGENT_CMD")); len(c) > 0 {
		return cmdAgent{argv: c}
	}
	return &httpAgent{poll: 2 * time.Second, wait: agentWait}
}

// pdhEnvPath: .env des PDH.
func pdhEnvPath() string {
	if p := os.Getenv("PDH_HA_PDH_ENV"); p != "" {
		return p
	}
	return filepath.Join(pdhRepo(), ".env")
}

// pdhEnv liefert einen Wert: zuerst aus der Umgebung von pdh-ha (Labor),
// sonst aus der .env des PDH.
func pdhEnv(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	f, err := os.Open(pdhEnvPath())
	if err != nil {
		return ""
	}
	defer f.Close()
	val := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		val = v // der letzte Eintrag gilt (wie bei der Shell)
	}
	return val
}

// cmdAgent ruft einen Befehl auf: <argv…> install|install_norestart|
// rollback|rollback_norestart <commit> bzw. <argv…> finish.
type cmdAgent struct{ argv []string }

func (c cmdAgent) run(ctx context.Context, args ...string) error {
	// Das Ende des Dienstes bricht den Befehl nicht ab: Er startet pdh-ha
	// womöglich selbst neu (Einspielen), ein Abbruch ließe ihn halb fertig.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentWait)
	defer cancel()
	argv := append(append([]string(nil), c.argv...), args...)
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, lastLines(string(out), 5))
	}
	return nil
}

func (c cmdAgent) Install(ctx context.Context, commit string, noRestart bool) error {
	op := "install"
	if noRestart {
		op = "install_norestart"
	}
	return c.run(ctx, op, commit)
}

func (c cmdAgent) Rollback(ctx context.Context, commit string, noRestart bool) error {
	if commit == "" {
		return errors.New("Zurückdrehen ohne Alt-Stand verweigert")
	}
	op := "rollback"
	if noRestart {
		op = "rollback_norestart"
	}
	return c.run(ctx, op, commit)
}

func (c cmdAgent) Finish(ctx context.Context) error { return c.run(ctx, "finish") }

// Busy: Der Labor-Agent führt seine Läufe nacheinander aus (Sperrdatei);
// ein Zurückdrehen wartet dort ohnehin auf ein laufendes Einspielen.
func (c cmdAgent) Busy(context.Context) (bool, error) { return false, nil }

// httpAgent: POST /v1/update, danach GET /v1/status bis running=false.
// Adresse und Token werden bei jedem Aufruf aus der .env gelesen.
type httpAgent struct {
	poll, wait time.Duration
	client     *http.Client
}

// agentUpdateReq ist der Rumpf von POST /v1/update.
type agentUpdateReq struct {
	Commit    string `json:"commit,omitempty"`
	Rollback  string `json:"rollback,omitempty"`
	NoRestart bool   `json:"no_restart,omitempty"`
	Finish    bool   `json:"finish,omitempty"`
}

func (h *httpAgent) Install(ctx context.Context, commit string, noRestart bool) error {
	return h.run(ctx, agentUpdateReq{Commit: commit, NoRestart: noRestart})
}

func (h *httpAgent) Rollback(ctx context.Context, commit string, noRestart bool) error {
	if commit == "" {
		// I5: ohne Ziel würde der Agent den neuesten Stand einspielen
		return errors.New("Zurückdrehen ohne Alt-Stand verweigert")
	}
	return h.run(ctx, agentUpdateReq{Rollback: commit, NoRestart: noRestart})
}

// op: Art des Laufs, wie sie der Agent in /v1/status meldet.
func (r agentUpdateReq) op() string {
	suffix := ""
	if r.NoRestart {
		suffix = "_norestart"
	}
	switch {
	case r.Finish:
		return "finish"
	case r.Rollback != "":
		return "rollback" + suffix
	case r.Commit != "":
		return "install" + suffix
	}
	return ""
}

func (r agentUpdateReq) target() string {
	if r.Rollback != "" {
		return r.Rollback
	}
	return r.Commit
}

// agentRun: Lauf laut Agent (409-Rumpf bzw. /v1/status).
type agentRun struct {
	Running bool   `json:"running"`
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Commit  string `json:"commit"`
	Op      string `json:"op"`
}

// known: meldet der Agent Stand bzw. Art des Laufs? (ältere Agenten nicht)
func (st agentRun) known() bool { return st.Commit != "" || st.Op != "" }

// mine: gehört der Lauf zu dieser Anfrage? Meldet der Agent weder Stand
// noch Art (älterer Agent), lässt sich das nicht prüfen – dann wie bisher.
// Meldet er die Art nicht, zählt nur der Stand.
func (st agentRun) mine(r agentUpdateReq) bool {
	if !st.known() {
		return true
	}
	if st.Commit != r.target() {
		return false
	}
	return st.Op == "" || st.Op == r.op()
}

func (h *httpAgent) Finish(ctx context.Context) error {
	return h.run(ctx, agentUpdateReq{Finish: true})
}

func (h *httpAgent) do(ctx context.Context, method, url, token string, body []byte) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c := h.client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

func (h *httpAgent) run(ctx context.Context, r agentUpdateReq) error {
	base := strings.TrimRight(pdhEnv("PDH_UPDATE_AGENT_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:8091"
	}
	token := pdhEnv("PDH_UPDATE_AGENT_TOKEN")
	if token == "" {
		return errors.New("Token des Update-Agenten fehlt (PDH_UPDATE_AGENT_TOKEN in " + pdhEnvPath() + ")")
	}
	body, _ := json.Marshal(r)
	wait := h.wait
	if wait <= 0 {
		wait = agentWait
	}
	deadline := time.Now().Add(wait)
	// Der Agent startet nach jedem Lauf kurz neu: Verbindungsfehler beim
	// Senden so lange wiederholen, bevor sie als Fehler zählen.
	reachUntil := time.Now().Add(agentRestartGrace)
	if reachUntil.After(deadline) {
		reachUntil = deadline
	}
	for {
		code, b, err := h.do(ctx, http.MethodPost, base+"/v1/update", token, body)
		if err != nil {
			if time.Now().Before(reachUntil) {
				if err := sleepCtx(ctx, h.poll); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("Update-Agent nicht erreichbar: %w", err)
		}
		if code != http.StatusAccepted && code != http.StatusOK && code != http.StatusConflict {
			return fmt.Errorf("Update-Agent: HTTP %d: %s", code, strings.TrimSpace(string(b)))
		}
		// 409: ein Lauf läuft schon. Derselbe Auftrag (Wiederholung nach dem
		// Neustart von pdh-ha): dessen Ergebnis zählt. Ein fremder Lauf (I3):
		// dessen Ende abwarten, dann den eigenen Auftrag erneut senden.
		var busy agentRun
		foreign := code == http.StatusConflict && json.Unmarshal(b, &busy) == nil && busy.known() && !busy.mine(r)
		st, err := h.waitIdle(ctx, base, token, deadline)
		if err != nil {
			return err
		}
		if !foreign && st.mine(r) {
			if st.Success {
				return nil
			}
			return fmt.Errorf("Update-Agent meldet Fehler: %s", lastLines(st.Output, 5))
		}
		// fremdes Ergebnis: zählt nicht – erneut senden
		if !time.Now().Before(deadline) {
			return errors.New("Update nicht rechtzeitig abgeschlossen (anderer Lauf beim Agenten)")
		}
		if err := sleepCtx(ctx, h.poll); err != nil {
			return err
		}
	}
}

// Busy fragt /v1/status einmal.
func (h *httpAgent) Busy(ctx context.Context) (bool, error) {
	base := strings.TrimRight(pdhEnv("PDH_UPDATE_AGENT_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:8091"
	}
	token := pdhEnv("PDH_UPDATE_AGENT_TOKEN")
	if token == "" {
		return false, errors.New("Token des Update-Agenten fehlt")
	}
	code, b, err := h.do(ctx, http.MethodGet, base+"/v1/status", token, nil)
	if err != nil {
		return false, err
	}
	if code != http.StatusOK {
		return false, fmt.Errorf("Update-Agent: HTTP %d", code)
	}
	var st agentRun
	if err := json.Unmarshal(b, &st); err != nil {
		return false, err
	}
	return st.Running, nil
}

// waitIdle fragt /v1/status, bis kein Lauf mehr läuft, und liefert dessen Stand.
func (h *httpAgent) waitIdle(ctx context.Context, base, token string, deadline time.Time) (agentRun, error) {
	var lastErr error
	for {
		code, b, err := h.do(ctx, http.MethodGet, base+"/v1/status", token, nil)
		switch {
		case err != nil:
			lastErr = err // Agent startet beim Update neu: weiter fragen
		case code != http.StatusOK:
			lastErr = fmt.Errorf("HTTP %d", code)
		default:
			var st agentRun
			if err := json.Unmarshal(b, &st); err != nil {
				lastErr = err
				break
			}
			if !st.Running {
				return st, nil
			}
		}
		if !time.Now().Before(deadline) {
			if lastErr != nil {
				return agentRun{}, fmt.Errorf("Update nicht rechtzeitig abgeschlossen (%v)", lastErr)
			}
			return agentRun{}, errors.New("Update nicht rechtzeitig abgeschlossen")
		}
		if err := sleepCtx(ctx, h.poll); err != nil {
			return agentRun{}, err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// lastLines: die letzten n nicht leeren Zeilen, für Meldungen.
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// pdhHealth: Antwort von GET /health des eigenen pdh.
type pdhHealth struct {
	Status       int
	Version      string
	MigrationsOK *bool // fehlt bei älteren Versionen
}

// up: pdh antwortet und meldet keine offenen Datenbank-Änderungen.
func (h pdhHealth) up() bool {
	return h.Status == http.StatusOK && (h.MigrationsOK == nil || *h.MigrationsOK)
}

// ok: up und erwartete Version.
func (h pdhHealth) ok(want string) bool { return h.up() && want != "" && h.Version == want }

// getPDHHealth fragt http://127.0.0.1:<PDH_SERVER_PORT, Standard 8090>/health.
func getPDHHealth(ctx context.Context) (pdhHealth, error) {
	port := pdhEnv("PDH_SERVER_PORT")
	if port == "" {
		port = "8090"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/health", nil)
	if err != nil {
		return pdhHealth{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return pdhHealth{}, err
	}
	defer resp.Body.Close()
	h := pdhHealth{Status: resp.StatusCode}
	if resp.StatusCode == http.StatusOK {
		var b struct {
			Version      string `json:"version"`
			MigrationsOK *bool  `json:"migrations_ok"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&b); err != nil {
			return h, err
		}
		h.Version, h.MigrationsOK = b.Version, b.MigrationsOK
	}
	return h, nil
}

// pdhHealthOK: pdh antwortet mit 200, Version wantVersion und
// migrations_ok=true (fehlt das Feld, zählt nur die Version).
func pdhHealthOK(ctx context.Context, wantVersion string) bool {
	h, err := getPDHHealth(ctx)
	return err == nil && h.ok(wantVersion)
}
