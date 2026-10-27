package main

// Ausfallschutz: Besitzer des Sicherungsordners. Der Ordner heißt wie der
// Server (<Laufwerk>/PDH-Ausfallschutz/<hostname>) – heißen zwei Server gleich,
// schrieben beide hinein: gleichnamige WAL-Dateien, gemischte Grundsicherungen,
// gegenseitig gelöschte Dateien. besitzer.json hält die Kennung der Datenbank
// (system_identifier) fest; ein Server mit anderer Datenbank sichert dort nicht.
// Beide Knoten eines Verbunds haben dieselbe Kennung (Spiegel).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const drOwnerFile = "besitzer.json"

type drOwner struct {
	SystemID string    `json:"system_id"`
	Host     string    `json:"host"`
	Since    time.Time `json:"since"`
}

const drSystemIDScript = `runuser -u postgres -- psql -XAtqc 'SELECT system_identifier FROM pg_control_system()'`

// drSystemIDMaxAge: Kennung neu lesen (ändert sich nur, wenn die Datenbank
// neu angelegt oder von einem anderen Server kopiert wird).
const drSystemIDMaxAge = 10 * time.Minute

// drSystemID: Kennung der Datenbank dieses Servers (zwischengespeichert).
func (a *updateAgent) drSystemID(ctx context.Context) (string, error) {
	if a.drSysID != nil {
		return a.drSysID(ctx)
	}
	a.dr.mu.Lock()
	id, at := a.dr.systemID, a.dr.systemIDAt
	a.dr.mu.Unlock()
	if id != "" && time.Since(at) < drSystemIDMaxAge {
		return id, nil
	}
	out, err := a.hostCommandEnv(ctx, false, drSystemIDScript, nil).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil || !drDigits(text) {
		lines := strings.Split(text, "\n")
		msg := lines[len(lines)-1]
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return "", errors.New("Kennung der Datenbank nicht lesbar: " + msg)
	}
	a.dr.mu.Lock()
	a.dr.systemID, a.dr.systemIDAt = text, time.Now()
	a.dr.mu.Unlock()
	return text, nil
}

func drDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// drCheckOwner: Ordner dir gehört der Datenbank id? Ohne besitzer.json (neuer
// Ordner oder Sicherung vor 0.76.1) wird er übernommen. shown ist der Pfad für
// die Meldung (ohne /proc/1/root).
func drCheckOwner(dir, shown, id, host string, now time.Time) error {
	p := filepath.Join(dir, drOwnerFile)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		b, _ = json.MarshalIndent(drOwner{SystemID: id, Host: host, Since: now.UTC()}, "", "  ")
		tmp := p + ".part"
		if err := os.WriteFile(tmp, b, 0o640); err != nil {
			return fmt.Errorf("%s nicht geschrieben: %w", drOwnerFile, err)
		}
		return os.Rename(tmp, p)
	}
	if err != nil {
		return fmt.Errorf("%s nicht lesbar: %w", drOwnerFile, err)
	}
	var o drOwner
	if json.Unmarshal(b, &o) != nil || o.SystemID == "" {
		return fmt.Errorf("%s im Sicherungsordner %s ist beschädigt – bitte prüfen", drOwnerFile, shown)
	}
	if o.SystemID != id {
		return fmt.Errorf("Sicherungsordner %s gehört einem anderen Server (%s, seit %s) – zwei Server mit gleichem Namen? Diesen Server umbenennen oder den Ordner auf dem Laufwerk umbenennen",
			shown, o.Host, o.Since.Local().Format("02.01.2006"))
	}
	return nil
}

// drOwnerCheck: Besitzer des Zielordners prüfen bzw. eintragen.
func (a *updateAgent) drOwnerCheck(ctx context.Context, target string) error {
	id, err := a.drSystemID(ctx)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	return drCheckOwner(a.hp(target), target, id, host, time.Now())
}
