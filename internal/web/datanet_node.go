package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"

	"pdh/internal/datanet"
)

// Einstellungen im Datennetz (app_settings).
const (
	dataNetKeyEnabled = "datanet.cloud.enabled"
	dataNetKeyAuth    = "datanet.cloud.authkey."
	dataNetKeyNode    = "datanet.cloud.node."
)

// dataNetUpTimeout ist die Zeitgrenze für den Aufbau der Verbindung.
const dataNetUpTimeout = 2 * time.Minute

// dataNetKey ist der Schlüssel für Zugangsschlüssel im Datennetz.
func (h *Handler) dataNetKey() []byte {
	secret := h.mailConfig().CredentialsKey
	if secret == "" {
		secret = h.jwtSecret
	}
	sum := sha256.Sum256([]byte("pdh-datanet:" + secret))
	return sum[:]
}

var dataNetNameRe = regexp.MustCompile(`[^a-z0-9-]+`)

// dataNetServerName ist der Rechnername, klein und nur aus [a-z0-9-].
func (h *Handler) dataNetServerName() string {
	n, _ := os.Hostname()
	n = strings.Trim(dataNetNameRe.ReplaceAllString(strings.ToLower(n), "-"), "-")
	if n == "" {
		n = "server"
	}
	return n
}

func (h *Handler) dataNetSetting(ctx context.Context, key string) string {
	var v string
	if err := h.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, key).Scan(&v); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Warn().Err(err).Str("schluessel", key).Msg("Datennetz: Einstellung nicht lesbar")
		}
		return ""
	}
	return v
}

func (h *Handler) dataNetPut(ctx context.Context, key, value string) error {
	_, err := h.db.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, value)
	return err
}

// dataNetSettings liefert, ob das Datennetz an ist, und den Zugangsschlüssel
// dieses Servers (leer, wenn keiner hinterlegt ist).
func (h *Handler) dataNetSettings(ctx context.Context) (enabled bool, authKey string) {
	enabled = h.dataNetSetting(ctx, dataNetKeyEnabled) == "true"
	enc := h.dataNetSetting(ctx, dataNetKeyAuth+h.dataNetServerName())
	if enc != "" {
		k, err := decryptSecret(h.dataNetKey(), enc)
		if err != nil {
			log.Warn().Err(err).Msg("Datennetz: Zugangsschlüssel nicht lesbar")
		} else {
			authKey = k
		}
	}
	return enabled, authKey
}

// dataNetSetAuthKey speichert den Zugangsschlüssel eines Servers verschlüsselt;
// ein leerer Schlüssel löscht ihn.
func (h *Handler) dataNetSetAuthKey(ctx context.Context, server, key string) error {
	enc := ""
	if key != "" {
		var err error
		if enc, err = encryptSecret(h.dataNetKey(), key); err != nil {
			return err
		}
	}
	return h.dataNetPut(ctx, dataNetKeyAuth+server, enc)
}

func dataNetStateDir() string {
	if d := os.Getenv("PDH_DATANET_DIR"); d != "" {
		return d
	}
	return "config/datanet/cloud"
}

// StartDataNet startet den Knoten im Hintergrund (nie blockierend; Fehler nur
// im Protokoll). Auf beiden Servern; den Rückkanal für Meldungen bedient nur
// der aktive Server (active).
func (h *Handler) StartDataNet(ctx context.Context, active bool) {
	h.StopDataNet() // ein Knoten je Server: einen früheren Lauf zuerst beenden
	newNode := h.dataNetNewNode
	if newNode == nil {
		newNode = datanet.NewTSNode
	}
	m := datanet.NewManager(newNode, dataNetUpTimeout)
	ctx, cancel := context.WithCancel(ctx)
	h.dataNetMu.Lock()
	h.dataNet, h.dataNetCancel, h.dataNetSrv = m, cancel, nil
	h.dataNetMu.Unlock()
	setConnNetResolve(h.dataNetResolve) // Weg „Datennetz“ der Verbindungen (conn_net.go)
	if h.db == nil {
		return
	}
	go func() {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		enabled, authKey := h.dataNetSettings(sctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if !enabled {
			return
		}
		server := h.dataNetServerName()
		m.Start(datanet.NodeConfig{Dir: dataNetStateDir(), Hostname: "pdh-" + server, AuthKey: authKey},
			func(n datanet.Node) { h.dataNetUp(ctx, n, server, active) })
		if ctx.Err() != nil { // StopDataNet kam während des Lesens der Einstellungen
			m.Stop()
		}
	}()
}

// dataNetUp läuft, sobald der Knoten verbunden ist.
func (h *Handler) dataNetUp(ctx context.Context, n datanet.Node, server string, active bool) {
	h.dataNetMu.Lock()
	if ctx.Err() != nil { // schon gestoppt
		h.dataNetMu.Unlock()
		return
	}
	h.dataNetBridges = datanet.NewBridges(n)
	h.dataNetMu.Unlock()
	if !active {
		return // Spiegel: online, aber kein Rückkanal und keine Schreibzugriffe
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st := h.dataNetStatus()
	addrs := make([]string, 0, len(st.Addrs))
	for _, a := range st.Addrs {
		addrs = append(addrs, a.String())
	}
	j, _ := json.Marshal(map[string]any{"addrs": addrs, "hostname": "pdh-" + server, "seen": time.Now().UTC().Format(time.RFC3339)})
	if err := h.dataNetPut(wctx, dataNetKeyNode+server, string(j)); err != nil {
		log.Warn().Err(err).Msg("Datennetz: Knotendaten nicht gespeichert")
	}
	// Der Zugangsschlüssel wird nur einmal gebraucht; der Zustand liegt jetzt lokal.
	if err := h.dataNetSetAuthKey(wctx, server, ""); err != nil {
		log.Warn().Err(err).Msg("Datennetz: Zugangsschlüssel nicht gelöscht")
	}
	addr := h.dataNetListenAddr
	if addr == "" {
		addr = ":8443"
	}
	ln, err := n.Listen("tcp", addr)
	if err != nil {
		log.Warn().Err(err).Msg("Datennetz: Rückkanal nicht gestartet")
		return
	}
	srv := &http.Server{Handler: h.dataNetReportHandler(), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	h.dataNetMu.Lock()
	if ctx.Err() != nil {
		h.dataNetMu.Unlock()
		ln.Close()
		return
	}
	h.dataNetSrv = srv
	h.dataNetMu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Debug().Err(err).Msg("Datennetz: Rückkanal beendet")
		}
	}()
}

func (h *Handler) dataNetStatus() datanet.Status {
	h.dataNetMu.RLock()
	m := h.dataNet
	h.dataNetMu.RUnlock()
	if m == nil {
		return datanet.Status{State: datanet.StateOff}
	}
	return m.Status()
}

// StopDataNet beendet Knoten und Brücken.
func (h *Handler) StopDataNet() {
	h.dataNetMu.Lock()
	m, b, cancel, srv := h.dataNet, h.dataNetBridges, h.dataNetCancel, h.dataNetSrv
	h.dataNetBridges, h.dataNetSrv = nil, nil
	if cancel != nil {
		cancel() // unter dem Lock: dataNetUp legt danach nichts mehr an
	}
	h.dataNetMu.Unlock()
	if srv != nil {
		sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(sctx)
		scancel()
	}
	if b != nil {
		b.Close()
	}
	if m != nil {
		m.Stop()
	}
}
