package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"pdh/internal/datanet"
)

const (
	upRetryStart   = 10 * time.Second
	upRetryMax     = 5 * time.Minute
	probeTimeout   = 5 * time.Second
	reportTimeout  = 15 * time.Second
	reportRetry    = 30 * time.Second
	peerTagTimeout = 5 * time.Second
)

// configError kennzeichnet Fehler, die ein Neustart nicht behebt (fehlender
// Schlüssel, ungültige Konfiguration). Der Prozess endet dann mit exitConfig,
// damit Dienstverwaltungen ihn nicht in einer Schleife neu starten.
type configError struct{ error }

func (e configError) Unwrap() error { return e.error }

const exitConfig = 3

// exitCode liefert den Prozess-Exitcode zu einem Fehler.
func exitCode(err error) int {
	var ce configError
	if errors.As(err, &ce) {
		return exitConfig
	}
	return 1
}

// Pausen, in Tests verkürzt.
var (
	listenRetry = 60 * time.Second
	saveRetries = 3
	savePause   = 2 * time.Second
)

// state hält Fehler, die in die Meldung einfließen.
type state struct {
	mu        sync.Mutex
	listenErr map[int]string // Port -> Fehler
	note      string         // Fehler, der alle Ziele betrifft
}

func (s *state) setListen(port int, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listenErr == nil {
		s.listenErr = map[int]string{}
	}
	if msg == "" {
		delete(s.listenErr, port)
	} else {
		s.listenErr[port] = msg
	}
}

func (s *state) setNote(msg string) {
	s.mu.Lock()
	s.note = msg
	s.mu.Unlock()
}

func (s *state) problem(port int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.note != "" {
		return s.note
	}
	return s.listenErr[port]
}

// Run verbindet den Knoten mit dem Datennetz, leitet die freigegebenen Ports an
// die lokalen Ziele weiter (nur für den PDH-Server) und meldet regelmäßig den
// Zustand der Ziele. Run kehrt zurück, wenn ctx endet, oder mit einem Fehler,
// wenn der Start nicht möglich ist. save schreibt die Konfiguration, nachdem der
// Einmal-Schlüssel verbraucht wurde.
func Run(ctx context.Context, c Config, node datanet.Node, save func(Config) error) error {
	if c.AuthKey == "" && !datanet.HasState(c.StateDir) {
		return configError{errors.New("Einmal-Schlüssel fehlt und es gibt noch keine Anmeldung im Datennetz (Dienst mit \"install --authkey …\" neu einrichten)")}
	}

	if err := upWithRetry(ctx, node); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	var st state
	if c.AuthKey != "" {
		c.AuthKey = ""
		if err := saveWithRetry(ctx, c, save); err != nil {
			msg := "Einmal-Schlüssel konnte nicht aus der Konfiguration entfernt werden: " + err.Error()
			log.Printf("FEHLER: %s", msg)
			st.setNote(msg)
		}
	}
	log.Printf("Im Datennetz verbunden (Version %s)", version)

	var wg sync.WaitGroup
	defer wg.Wait()
	fctx, stop := context.WithCancel(ctx)
	defer stop()

	allow := func(remote net.Addr) bool {
		tctx, cancel := context.WithTimeout(fctx, peerTagTimeout)
		defer cancel()
		tags, err := node.PeerTags(tctx, remote.String())
		return err == nil && slices.Contains(tags, datanet.TagPDH)
	}
	var first sync.WaitGroup // erster Listen-Versuch je Ziel, damit die erste Meldung stimmt
	for _, t := range c.Targets {
		wg.Add(1)
		first.Add(1)
		go func(t datanet.Target) {
			defer wg.Done()
			serveTarget(fctx, node, t, allow, &st, first.Done)
		}(t)
	}
	first.Wait()

	reportLoop(ctx, c, node, &st)
	return nil
}

// saveWithRetry versucht save mehrfach.
func saveWithRetry(ctx context.Context, c Config, save func(Config) error) error {
	var err error
	for i := 0; i < saveRetries; i++ {
		if err = save(c); err == nil {
			return nil
		}
		log.Printf("Konfiguration speichern fehlgeschlagen (Versuch %d/%d): %v", i+1, saveRetries, err)
		if i == saveRetries-1 {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(savePause):
		}
	}
	return err
}

// serveTarget belauscht den Port im Datennetz und leitet weiter; ist der Port
// nicht belegbar, wird nach listenRetry erneut versucht.
func serveTarget(ctx context.Context, node datanet.Node, t datanet.Target, allow func(net.Addr) bool, st *state, attempted func()) {
	var once sync.Once
	defer once.Do(attempted)
	for ctx.Err() == nil {
		ln, err := node.Listen("tcp", ":"+strconv.Itoa(t.Port))
		if err != nil {
			msg := fmt.Sprintf("Port %d nicht verfügbar: %v", t.Port, err)
			log.Print(msg)
			st.setListen(t.Port, msg)
			once.Do(attempted)
			select {
			case <-ctx.Done():
				return
			case <-time.After(listenRetry):
			}
			continue
		}
		st.setListen(t.Port, "")
		once.Do(attempted)
		datanet.Forward(ctx, ln, t.Addr, allow)
		// Forward endet nur bei ctx-Ende oder Listener-Fehler; dann neu belauschen.
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// upWithRetry verbindet, bei Fehlern mit wachsender Pause (10 s bis 5 min).
func upWithRetry(ctx context.Context, node datanet.Node) error {
	wait := upRetryStart
	for {
		_, err := node.Up(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("Datennetz nicht erreichbar: %v – neuer Versuch in %s", err, wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, upRetryMax)
	}
}

func reportLoop(ctx context.Context, c Config, node datanet.Node, st *state) {
	hc := &http.Client{
		Transport: &http.Transport{DialContext: node.DialContext},
		Timeout:   reportTimeout,
	}
	defer hc.CloseIdleConnections()
	for {
		wait := reportRetry
		if resp, err := reportOnce(ctx, hc, c, st); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Meldung an den PDH-Server fehlgeschlagen: %v", err)
		} else {
			wait = datanet.ClampInterval(resp.IntervalSeconds)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func reportOnce(ctx context.Context, hc *http.Client, c Config, st *state) (datanet.ReportResponse, error) {
	rep := datanet.Report{
		ClientID: c.ClientID, Version: version, OS: runtime.GOOS, Hostname: c.Hostname,
	}
	for _, t := range c.Targets {
		ts := datanet.TargetStatus{Port: t.Port, OK: true, CheckedAt: time.Now().UTC()}
		if msg := st.problem(t.Port); msg != "" {
			ts.OK, ts.Error = false, msg
		} else if err := datanet.Probe(ctx, t.Addr, probeTimeout); err != nil {
			ts.OK, ts.Error = false, err.Error()
		}
		rep.Targets = append(rep.Targets, ts)
	}
	body, err := json.Marshal(rep)
	if err != nil {
		return datanet.ReportResponse{}, err
	}
	var errs []error
	for _, addr := range c.PDH {
		resp, err := postReport(ctx, hc, addr, c.Token, body)
		if err == nil {
			return resp, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", addr, err))
	}
	return datanet.ReportResponse{}, errors.Join(errs...)
}

func postReport(ctx context.Context, hc *http.Client, addr, token string, body []byte) (datanet.ReportResponse, error) {
	var out datanet.ReportResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+datanet.ReportPath, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return out, fmt.Errorf("Antwort %s", resp.Status)
	}
	// Eine unlesbare Antwort ist kein Fehler: die Meldung ist angekommen.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&out)
	return out, nil
}
