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

// Run verbindet den Knoten mit dem Datennetz, leitet die freigegebenen Ports an
// die lokalen Ziele weiter (nur für den PDH-Server) und meldet regelmäßig den
// Zustand der Ziele. Run kehrt zurück, wenn ctx endet, oder mit einem Fehler,
// wenn der Start nicht möglich ist. save schreibt die Konfiguration, nachdem der
// Einmal-Schlüssel verbraucht wurde.
func Run(ctx context.Context, c Config, node datanet.Node, save func(Config) error) error {
	if c.AuthKey == "" && !datanet.HasState(c.StateDir) {
		return errors.New("Einmal-Schlüssel fehlt und es gibt noch keine Anmeldung im Datennetz (Dienst mit \"install --authkey …\" neu einrichten)")
	}

	if err := upWithRetry(ctx, node); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if c.AuthKey != "" {
		c.AuthKey = ""
		if err := save(c); err != nil {
			log.Printf("Einmal-Schlüssel konnte nicht aus der Konfiguration entfernt werden: %v", err)
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
	for _, t := range c.Targets {
		ln, err := node.Listen("tcp", ":"+strconv.Itoa(t.Port))
		if err != nil {
			stop()
			return fmt.Errorf("Port %d im Datennetz nicht belegbar: %w", t.Port, err)
		}
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			datanet.Forward(fctx, ln, addr, allow)
		}(t.Addr)
	}

	reportLoop(ctx, c, node)
	return nil
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

func reportLoop(ctx context.Context, c Config, node datanet.Node) {
	hc := &http.Client{
		Transport: &http.Transport{DialContext: node.DialContext},
		Timeout:   reportTimeout,
	}
	defer hc.CloseIdleConnections()
	for {
		wait := reportRetry
		if resp, err := reportOnce(ctx, hc, c); err != nil {
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

func reportOnce(ctx context.Context, hc *http.Client, c Config) (datanet.ReportResponse, error) {
	rep := datanet.Report{
		ClientID: c.ClientID, Version: version, OS: runtime.GOOS, Hostname: c.Hostname,
	}
	for _, t := range c.Targets {
		st := datanet.TargetStatus{Port: t.Port, OK: true, CheckedAt: time.Now().UTC()}
		if err := datanet.Probe(ctx, t.Addr, probeTimeout); err != nil {
			st.OK, st.Error = false, err.Error()
		}
		rep.Targets = append(rep.Targets, st)
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
