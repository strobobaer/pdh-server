package web

import (
	"context"
	"errors"
	"sync"

	"pdh/internal/integrations/omada"
)

// Anbindung an einen TP-Link-Omada-Controller im Haus (Zusatz, 0.80.21): Einstellungen
// und der gemeinsame Zugriff für den IT-Netzabgleich. Der Reiter „Omada“ der
// Server-Einstellungen steht in omada_web.go.

const (
	permOmada = "system.omada"

	keyOmadaURL    = "omada.url"
	keyOmadaID     = "omada.id"     // Omada-ID des Controllers
	keyOmadaClient = "omada.client" // Client-ID der Open-API-Anwendung
	keyOmadaSecret = "omada.secret" // Client-Secret, verschlüsselt
	keyOmadaSite   = "omada.site"   // Name des Standorts (leer: der einzige)
	keyOmadaPin    = "omada.pin"    // bestätigter Fingerabdruck des Zertifikats
)

var errOmadaOff = errors.New("Omada ist nicht eingerichtet. In den Server-Einstellungen im Reiter „Omada“ Adresse und Zugangsdaten speichern")

// omadaConn hält den Client über Läufe hinweg, damit Zugriffstoken und Standort nicht bei
// jedem Abgleich neu geholt werden. Ändert sich eine Einstellung, entsteht ein neuer Client.
type omadaConn struct {
	mu     sync.Mutex
	key    string
	client *omada.Client
}

// omadaConnected: Adresse, Zugangsdaten und bestätigter Fingerabdruck sind hinterlegt.
func (h *Handler) omadaConnected(ctx context.Context) bool {
	for _, k := range []string{keyOmadaURL, keyOmadaID, keyOmadaClient, keyOmadaSecret, keyOmadaPin} {
		if h.certSetting(ctx, k) == "" {
			return false
		}
	}
	return true
}

// omadaAPI: Client für den Controller aus den Einstellungen; errOmadaOff, wenn nichts
// eingerichtet ist (dann geht keine Anfrage hinaus).
func (h *Handler) omadaAPI(ctx context.Context) (*omada.Client, error) {
	base, id, cid := h.certSetting(ctx, keyOmadaURL), h.certSetting(ctx, keyOmadaID), h.certSetting(ctx, keyOmadaClient)
	enc := h.certSetting(ctx, keyOmadaSecret)
	if base == "" || id == "" || cid == "" || enc == "" {
		return nil, errOmadaOff
	}
	secret, err := decryptSecret(h.pkiKey(), enc)
	if err != nil || secret == "" {
		return nil, errors.New("Das Omada-Secret ist nicht lesbar – bitte in den Server-Einstellungen im Reiter „Omada“ neu eintragen")
	}
	pin := h.certSetting(ctx, keyOmadaPin)
	if pin == "" {
		return nil, omada.ErrNoPin
	}
	site := h.certSetting(ctx, keyOmadaSite)
	// enc ändert sich mit jedem neu gespeicherten Secret (auch bei gleichem Klartext) – gut genug als Marke.
	key := base + "\x00" + id + "\x00" + cid + "\x00" + enc + "\x00" + site + "\x00" + pin
	h.omadaC.mu.Lock()
	defer h.omadaC.mu.Unlock()
	if h.omadaC.client == nil || h.omadaC.key != key {
		h.omadaC.client, h.omadaC.key = omada.New(base, id, cid, secret, site, pin), key
	}
	return h.omadaC.client, nil
}
