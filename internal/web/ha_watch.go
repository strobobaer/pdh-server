package web

// Alarme des Ausfallschutzes (pdh-ha): Ereignisse der Art „alarm“ gehen als
// PDH-Benachrichtigung an die Admins – je Text-Art höchstens eine je 15 min
// (Spezifikation §6).

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"time"
)

const (
	haSetEventSeq   = "ha.event_seq"   // letzte verarbeitete Ereignis-Nummer
	haSetAlarmLast  = "ha.alarm_last." // + Schlüssel: letzte Meldung (RFC3339)
	haAlarmThrottle = 15 * time.Minute
	haWatchEvery    = 30 * time.Second
)

func parseSeq(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// haAlarmKey: SHA-1 der ersten 40 Zeichen – gleiche Alarm-Art, gleicher Schlüssel.
func haAlarmKey(text string) string {
	r := []rune(text)
	if len(r) > 40 {
		r = r[:40]
	}
	sum := sha1.Sum([]byte(string(r)))
	return hex.EncodeToString(sum[:])
}

// haNotifyAlarms meldet Alarme gedrosselt. recentOnly (erster Lauf ohne
// Prüfpunkt): nur Alarme der letzten 15 min, alte nicht nachmelden.
func haNotifyAlarms(evs []haEvent, now time.Time, recentOnly bool, get func(string) string, set func(string, string), notify func(string)) {
	for _, e := range evs {
		if e.Kind != "alarm" || (recentOnly && now.Sub(e.At) > haAlarmThrottle) {
			continue
		}
		key := haSetAlarmLast + haAlarmKey(e.Text)
		if last, err := time.Parse(time.RFC3339, get(key)); err == nil && now.Sub(last) < haAlarmThrottle {
			continue
		}
		set(key, now.UTC().Format(time.RFC3339))
		notify("⚠️ **Ausfallschutz**\n" + e.Text + "\nServer-Einstellungen → Ausfallschutz")
	}
}

// haWatchOnce holt die Ereignisse seit dem Prüfpunkt und meldet die Alarme.
func (h *Handler) haWatchOnce(ctx context.Context, now time.Time, get func(string) string, set func(string, string), notify func(string)) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw := get(haSetEventSeq)
	cp := parseSeq(raw)
	recentOnly := raw == ""
	if cp > 0 { // Protokoll neu begonnen (Ereignis cp gibt es nicht mehr)?
		evs, err := h.haEvents(ctx, cp-1, 1)
		if err != nil {
			return
		}
		if len(evs) == 0 || evs[0].Seq != cp {
			cp, recentOnly = 0, true
		}
	}
	for page := 0; page < 10; page++ {
		evs, err := h.haEvents(ctx, cp, 1000)
		if err != nil || len(evs) == 0 {
			break
		}
		haNotifyAlarms(evs, now, recentOnly, get, set, notify)
		cp = evs[len(evs)-1].Seq
		set(haSetEventSeq, strconv.FormatUint(cp, 10))
		if len(evs) < 1000 {
			break
		}
	}
	if raw == "" && get(haSetEventSeq) == "" {
		set(haSetEventSeq, "0") // Prüfpunkt angelegt: ab jetzt alles melden
	}
}

// haWatch: alle 30 s, nur wenn pdh-ha installiert ist.
func (h *Handler) haWatch(ctx context.Context) {
	t := time.NewTicker(haWatchEvery)
	defer t.Stop()
	get := func(k string) string { return h.appSetting(ctx, k, "") }
	set := func(k, v string) { _ = h.setAppSetting(ctx, k, v) }
	notify := func(msg string) { h.notifyAdmins(ctx, msg) }
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if h.db == nil || !haInstalled() || h.maintSkip("ausfallschutz-alarme") {
			continue
		}
		h.haWatchOnce(ctx, time.Now(), get, set, notify)
	}
}
