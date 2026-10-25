package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	joinCodeTTL   = 30 * time.Minute
	joinMaxFails  = 5
	joinFailSpan  = time.Minute
	joinCodeBytes = 32
)

// joinCode ist der Text, den der Anwender auf dem neuen Knoten einfügt.
type joinCode struct {
	Addrs   []string `json:"a"`
	FP      string   `json:"fp"` // Fingerabdruck der CA
	Secret  string   `json:"s"`
	Role    string   `json:"r"`
	Commit  string   `json:"c"`
	Cluster string   `json:"id"`
}

// String ist base64url(JSON) ohne Auffüllung.
func (j joinCode) String() string {
	b, _ := json.Marshal(j)
	return base64.RawURLEncoding.EncodeToString(b)
}

func parseJoinCode(s string) (joinCode, error) {
	var j joinCode
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return j, errors.New("Beitrittscode ist unlesbar")
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return j, errors.New("Beitrittscode ist unlesbar")
	}
	if len(j.Addrs) == 0 || j.FP == "" || j.Secret == "" || j.Cluster == "" {
		return joinCode{}, errors.New("Beitrittscode ist unvollständig")
	}
	return j, nil
}

type joinEntry struct {
	role    string
	expires time.Time
	used    bool
}

type failWindow struct {
	start time.Time
	n     int
}

// joinCodes verwaltet ausgegebene Codes nur im Speicher (als sha256 des
// Geheimnisses) und zählt Fehlversuche je Quelladresse und Minute.
// Der Nullwert ist benutzbar.
type joinCodes struct {
	mu    sync.Mutex
	codes map[[32]byte]*joinEntry
	fails map[string]*failWindow
}

// issue erzeugt ein neues Geheimnis (32 Byte) für die Rolle "node" | "witness".
func (j *joinCodes) issue(role string, now time.Time) (string, error) {
	if !validKind(role) {
		return "", fmt.Errorf("Ungültige Rolle %q", role)
	}
	b := make([]byte, joinCodeBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // ohne Zufall darf kein Code entstehen
	}
	secret := base64.RawURLEncoding.EncodeToString(b)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.codes == nil {
		j.codes = map[[32]byte]*joinEntry{}
	}
	// Abgelaufene Einträge aufräumen
	for k, e := range j.codes {
		if now.After(e.expires) {
			delete(j.codes, k)
		}
	}
	j.codes[sha256.Sum256([]byte(secret))] = &joinEntry{role: role, expires: now.Add(joinCodeTTL)}
	return secret, nil
}

// redeem löst das Geheimnis ein. Fehlversuche zählen je Quelladresse; ab dem
// fünften in einer Minute wird auch ein richtiger Code abgewiesen (er bleibt gültig).
func (j *joinCodes) redeem(secret, srcIP string, now time.Time) (role string, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fails == nil {
		j.fails = map[string]*failWindow{}
	}
	w := j.fails[srcIP]
	if w == nil || now.Sub(w.start) >= joinFailSpan {
		w = &failWindow{start: now}
		// alte Fenster anderer Quellen entsorgen
		for k, o := range j.fails {
			if now.Sub(o.start) >= joinFailSpan {
				delete(j.fails, k)
			}
		}
		j.fails[srcIP] = w
	}
	if w.n >= joinMaxFails {
		return "", errors.New("Zu viele Versuche, bitte eine Minute warten")
	}
	fail := func(msg string) (string, error) {
		w.n++
		return "", errors.New(msg)
	}
	e := j.codes[sha256.Sum256([]byte(secret))]
	if e == nil {
		return fail("Beitrittscode ungültig")
	}
	if e.used {
		return fail("Beitrittscode bereits benutzt")
	}
	if now.After(e.expires) {
		return fail("Beitrittscode abgelaufen")
	}
	e.used = true
	return e.role, nil
}

// open: mindestens ein ausgegebener Code ist noch gültig und unbenutzt.
func (j *joinCodes) open(now time.Time) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.codes {
		if !e.used && !now.After(e.expires) {
			return true
		}
	}
	return false
}
