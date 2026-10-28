package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"pdh/pkg/config"
)

// Cloudflare-Tunnel auf beiden Servern: Das Token liegt verschlüsselt in der
// Datenbank (auch am Spiegel lesbar). Das PDH reicht es an den lokalen pdh-ha
// weiter (Unix-Socket, PUT /v1/tunnel); pdh-ha muss nichts entschlüsseln und
// meldet nur die Prüfsumme des abgelegten Tokens. Läuft auf beiden Servern
// (aktiv und Spiegel) und liest nur aus der Datenbank.

const (
	tunnelSyncEvery   = 60 * time.Second
	tunnelLogRepeat   = time.Hour
	tunnelSyncTimeout = 20 * time.Second
)

// tokenSum: SHA-256 (hex) des Tokens selbst – dieselbe Rechnung wie in pdh-ha;
// "" für ein leeres Token.
func tokenSum(token string) string {
	if token == "" {
		return ""
	}
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// tunnelToken: das gespeicherte Token. ok=false, wenn es sich nicht verlässlich
// lesen ließ (Datenbankfehler, nicht entschlüsselbar) – dann darf nichts
// geändert werden, ein leeres Ergebnis würde den Tunnel sonst entfernen.
func (h *Handler) tunnelToken(ctx context.Context) (string, bool) {
	if h.tunnelTokenFn != nil {
		return h.tunnelTokenFn(ctx)
	}
	if config.FromProcessEnv(cfTokenKey) {
		return strings.TrimSpace(os.Getenv(cfTokenKey)), true
	}
	if h.db != nil {
		vals, err := config.LoadDBSettings(ctx, h.db)
		if err != nil {
			return "", false
		}
		if s, ok := vals[cfTokenKey]; ok {
			if s.DecryptErr != nil {
				return "", false
			}
			return strings.TrimSpace(s.Value), true
		}
	}
	tok, _ := h.nextStartValue(ctx, cfTokenKey)
	return strings.TrimSpace(tok), true
}

// syncTunnelOnce gleicht das Token einmal ab. Nichts zu tun (pdh-ha fehlt,
// nicht eingerichtet, Token gleich) ist kein Fehler.
func (h *Handler) syncTunnelOnce(ctx context.Context) error {
	if !haInstalled() {
		return nil
	}
	var st haStatus
	if err := h.haDo(ctx, http.MethodGet, "/v1/status", nil, &st); err != nil {
		return err
	}
	if !st.Configured {
		return nil
	}
	token, ok := h.tunnelToken(ctx)
	if !ok || tokenSum(token) == st.Access.Tunnel.Sum {
		return nil
	}
	return h.haDo(ctx, http.MethodPut, "/v1/tunnel", map[string]string{"token": token}, nil)
}

// RunTunnelSync gleicht beim Start und danach alle 60 s das Tunnel-Token mit
// pdh-ha ab, bis ctx endet. Fehler gehen nur ins Server-Protokoll, derselbe
// Text höchstens einmal je Stunde. Das Token wird nie protokolliert.
func (h *Handler) RunTunnelSync(ctx context.Context) {
	var (
		mu   sync.Mutex
		seen = map[string]time.Time{}
	)
	step := func() {
		cctx, cancel := context.WithTimeout(ctx, tunnelSyncTimeout)
		defer cancel()
		err := h.syncTunnelOnce(cctx)
		if err == nil {
			mu.Lock()
			clear(seen)
			mu.Unlock()
			return
		}
		msg := err.Error()
		mu.Lock()
		last, ok := seen[msg]
		due := !ok || time.Since(last) >= tunnelLogRepeat
		if due {
			seen[msg] = time.Now()
		}
		mu.Unlock()
		if due {
			componentLog("ausfallschutz").Warn().Err(err).Msg("tunnel-token: abgleich mit pdh-ha fehlgeschlagen")
		}
	}
	step()
	t := time.NewTicker(tunnelSyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			step()
		}
	}
}
