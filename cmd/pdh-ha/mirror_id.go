package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"pdh/internal/ha"
)

// Kennung eines gespiegelten Ordners (R57, R58): Jeder Quell- und Zielordner trägt die Datei
// ha.MirrorIDName mit einem Zufallswert. Der Empfänger speichert je Quelle den Wert seines
// eigenen Ordners in mirror-inbox.json; Absender und Empfänger desselben Servers nutzen denselben
// Eintrag, ein nie aktiver Standby hat bei der Übernahme also denselben Bezug. Beleg, „leeres
// Ziel“ und --delete gelten nur, wenn die Datei lesbar ist und zum gespeicherten Wert passt.
// Fehlt sie, ist sie anders oder nicht lesbar, ist es ein anderer Ordner (Datenträger getauscht
// oder nicht eingehängt). Die Datei wird in keiner Richtung übertragen (Ausschluss in
// ha.MirrorSources) und zählt bei Leer-Prüfungen nicht.

const mirrorIDBytes = 16 // 32 Hex-Zeichen

// idCheck ist das Ergebnis des Vergleichs von Kennungsdatei und gespeichertem Wert.
type idCheck int

const (
	idOK    idCheck = iota // passt, oder gerade übernommen bzw. angelegt, weil nichts gespeichert war
	idOther                // gespeicherter Wert, aber Datei fehlt, anders oder nicht lesbar: anderer Ordner
	idFail                 // nichts gespeichert, und die Kennung ist weder lesbar noch anlegbar
)

func (c idCheck) String() string {
	switch c {
	case idOK:
		return "Kennung passt"
	case idOther:
		return "anderer Ordner"
	}
	return "Kennung nicht anlegbar"
}

// mirrorIDPath: Pfad der Kennungsdatei einer Quelle in dir (ihrem Quell- oder Zielordner).
func mirrorIDPath(m ha.MirrorSource, dir string) string {
	return filepath.Join(dir, filepath.FromSlash(m.IDFile()))
}

// readMirrorID liest die Kennung: eine gewöhnliche Datei (kein Link) mit genau einem Hex-Wert.
// Alles andere ergibt "", false.
func readMirrorID(p string) (string, bool) {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 4*mirrorIDBytes {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(b))
	if len(s) != 2*mirrorIDBytes {
		return "", false
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", false
	}
	return s, true
}

// writeMirrorID legt eine neue Kennung an (der Ordner der Datei wird bei Bedarf angelegt) und
// liefert den Wert. Eine vorhandene Datei wird ersetzt.
func writeMirrorID(p string) (string, error) {
	var b [mirrorIDBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		return "", errors.New(p + " ist keine gewöhnliche Datei")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := writeFileAtomic(p, []byte(id+"\n"), 0o644); err != nil {
		return "", err
	}
	// R63 (U1): Besitzer und Gruppe wie der Ordner, nicht root – das System-Update von Nextcloud
	// (updater.phar als www-data) bricht sonst an der nicht schreibbaren Datei in config/ ab
	if err := chownLikeParent(p); err != nil {
		return "", fmt.Errorf("Besitzer der Kennung %s nicht gesetzt: %w", p, err)
	}
	return id, nil
}

// checkID (R57) vergleicht die Kennung des Ordners mit dem gespeicherten Wert der Quelle. Ohne
// gespeicherten Wert gilt der Ordner als neu: Eine vorhandene Kennung wird übernommen, eine
// fehlende angelegt. Das Ergebnis wird im Speicher gemerkt; proofOf, proofs und noteSource lesen
// nur diesen Merker (kein Dateizugriff im Takt). Läuft auf der Verbindung oder in der Goroutine
// des Arbeiters, nie im Takt (R14). Schreibt den Zustand nur bei Änderung.
func (m *mirrorInbox) checkID(label, idPath string) idCheck {
	cur, readable := readMirrorID(idPath)
	m.mu.Lock()
	stored := m.st.ID[label]
	m.mu.Unlock()
	if stored != "" {
		ok := readable && cur == stored
		m.setIDOK(label, ok)
		if ok {
			return idOK
		}
		return idOther
	}
	if !readable {
		id, err := writeMirrorID(idPath)
		if err != nil {
			m.setIDOK(label, false)
			m.logOnce("id-write:"+label, "warn", "Quelle "+label+": Kennung "+idPath+" nicht anlegbar: "+err.Error(), mirrorErrLogEvery)
			return idFail
		}
		cur = id
	}
	m.mu.Lock()
	if m.st.ID == nil {
		m.st.ID = map[string]string{}
	}
	m.st.ID[label] = cur
	m.mu.Unlock()
	m.setIDOK(label, true)
	m.save()
	return idOK
}

// renewID (R58): Die Bestätigung am aktiven Server schreibt eine neue Kennung in den Ordner und
// in den Zustand; der Ordner gilt ab jetzt als der richtige.
func (m *mirrorInbox) renewID(label, idPath string) error {
	id, err := writeMirrorID(idPath)
	if err != nil {
		m.setIDOK(label, false)
		return fmt.Errorf("Kennung für %s nicht geschrieben: %w", label, err)
	}
	m.mu.Lock()
	if m.st.ID == nil {
		m.st.ID = map[string]string{}
	}
	m.st.ID[label] = id
	m.mu.Unlock()
	m.setIDOK(label, true)
	m.save()
	return nil
}

// proofStored: ein Beleg steht im Zustand, auch wenn die Kennung noch nicht geprüft ist (nach einem
// Neustart): Dieser Server hat die Quelle einmal vollständig bekommen.
func (m *mirrorInbox) proofStored(label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.st.Proof[label]
	return ok
}

// proofLost (R63, U4): Der Beleg gilt nachweislich nicht: keiner gespeichert, oder die letzte
// Prüfung der Kennung passte nicht (anderer Ordner, nicht anlegbar). Eine nach dem Neustart noch
// ungeprüfte Kennung zählt nicht: Sie nimmt den übernommenen Erstabgleich nicht zurück.
func (m *mirrorInbox) proofLost(label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, stored := m.st.Proof[label]
	ok, checked := m.idOK[label]
	return !stored || checked && !ok
}

// recheckIDs (R63, U4): Am passiven Server nach dem Start einmal die Kennung jeder Quelle prüfen,
// für die ein Wert gespeichert ist (sonst gälte jeder Beleg erst nach dem nächsten Lauf des
// Partners). Legt keine Kennung an; ein fehlender Ordner zählt als anderer. Dateizugriff: nur in
// der Schleife der Spiegelung, nie im Takt (R14).
func (m *mirrorInbox) recheckIDs(srcs []ha.MirrorSource) {
	for _, s := range srcs {
		if m.hasID(s.Label) {
			m.checkID(s.Label, mirrorIDPath(s, path.Clean(s.Path)))
		}
	}
}

// hasID: für die Quelle ist ein Wert gespeichert (dieser Server kennt den Ordner).
func (m *mirrorInbox) hasID(label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.ID[label] != ""
}

func (m *mirrorInbox) setIDOK(label string, ok bool) {
	m.mu.Lock()
	m.idOK[label] = ok
	m.mu.Unlock()
}

// idOKLocked: die letzte Prüfung der Kennung passte (nur Speicher; nach einem Neustart erst nach
// der ersten Prüfung wahr). mu gehalten.
func (m *mirrorInbox) idOKLocked(label string) bool { return m.idOK[label] }
