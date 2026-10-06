package web

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Anwesenheit fuer die Seitenleiste „Aktivitaet“: wer hat PDH gerade offen?
// Online ist, wer eine Live-Verbindung (Chat) offen hat oder in den letzten
// presenceWindow eine Seite/Abfrage geladen hat – jede offene Seite fragt die
// Seitenleiste alle 30 s ab. „Zuletzt gesehen“ steht in users.last_seen_at
// (hoechstens einmal je Minute und Benutzer geschrieben).
const (
	presenceWindow    = 2 * time.Minute
	presenceSaveEvery = time.Minute
	presenceMaxIdle   = 60 // so viele Offline-Benutzer zeigt die Liste hoechstens
)

type presenceTracker struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	saved map[string]time.Time
}

var presence = &presenceTracker{seen: map[string]time.Time{}, saved: map[string]time.Time{}}

// presenceTouch: angemeldete Anfrage eines Benutzers (authMiddleware).
func (h *Handler) presenceTouch(userID string) {
	if userID == "" {
		return
	}
	now := time.Now()
	presence.mu.Lock()
	presence.seen[userID] = now
	save := now.Sub(presence.saved[userID]) >= presenceSaveEvery
	if save {
		presence.saved[userID] = now
	}
	presence.mu.Unlock()
	if save && h.db != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = h.db.Exec(ctx, `UPDATE users SET last_seen_at = NOW() WHERE id = $1::uuid`, userID)
		}()
	}
}

// presenceOnline: Benutzer ist gerade da (Live-Verbindung oder kuerzlich aktiv).
func (h *Handler) presenceOnline(userID string) bool {
	presence.mu.Lock()
	t, ok := presence.seen[userID]
	presence.mu.Unlock()
	if ok && time.Since(t) < presenceWindow {
		return true
	}
	return h.chat().isOnline(userID)
}

type presenceUser struct {
	ID, Name, Initials, Color string
	Me                        bool
	Online                    bool
	LastSeen                  string // „vor 5 Min.“, „gestern 14:20“ …
}

type presenceData struct {
	Online, Offline []presenceUser
	OfflineMore     int
}

var presenceColors = []string{"#4f6ef7", "#10b981", "#f59e0b", "#ef4444", "#7c3aed", "#0ea5e9", "#ec4899", "#14b8a6"}

// presenceAgo: kurze Zeitangabe fuer „zuletzt gesehen“ in der Sprache der Anfrage.
func presenceAgo(lang string, t *time.Time, now time.Time) string {
	if t == nil {
		return tr(lang, "noch nie angemeldet")
	}
	d := now.Sub(*t)
	switch {
	case d < presenceWindow:
		return tr(lang, "gerade eben")
	case d < time.Hour:
		return tr(lang, "vor %d Min.", int(d.Minutes()))
	case d < 12*time.Hour:
		return tr(lang, "vor %d Std.", int(d.Hours()))
	}
	lt := t.Local()
	y := now.AddDate(0, 0, -1)
	switch {
	case lt.Year() == now.Year() && lt.YearDay() == now.YearDay():
		return tr(lang, "heute %s", lt.Format("15:04"))
	case lt.Year() == y.Year() && lt.YearDay() == y.YearDay():
		return tr(lang, "gestern %s", lt.Format("15:04"))
	}
	return lt.Format("02.01.2006")
}

// PresenceWeb: GET /api/presence – Liste fuer die Seitenleiste (HTML-Ausschnitt).
func (h *Handler) PresenceWeb(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := getUser(r).ID
	lang := h.requestLang(r)
	now := time.Now()
	data := presenceData{}
	rows, err := h.db.Query(ctx, `SELECT id::text, COALESCE(first_name,''), COALESCE(last_name,''), username, last_seen_at
		FROM users WHERE active AND NOT is_system_user`)
	if err == nil {
		var all []presenceUser
		seen := map[string]*time.Time{}
		for rows.Next() {
			var u presenceUser
			var first, lastName, user string
			var last *time.Time
			if rows.Scan(&u.ID, &first, &lastName, &user, &last) != nil {
				continue
			}
			u.Name = strings.TrimSpace(first + " " + lastName)
			if u.Name == "" {
				u.Name, first = user, user
			}
			u.Me = u.ID == me
			u.Online = u.Me || h.presenceOnline(u.ID)
			u.Initials = initials(first, lastName)
			var hsum uint32
			for _, c := range u.ID {
				hsum = hsum*31 + uint32(c)
			}
			u.Color = presenceColors[hsum%uint32(len(presenceColors))]
			u.LastSeen = presenceAgo(lang, last, now)
			seen[u.ID] = last
			all = append(all, u)
		}
		rows.Close()
		sort.Slice(all, func(i, j int) bool {
			a, b := all[i], all[j]
			if a.Online != b.Online {
				return a.Online
			}
			if a.Online {
				if a.Me != b.Me {
					return a.Me
				}
				return strings.ToLower(a.Name) < strings.ToLower(b.Name)
			}
			// offline: zuletzt Gesehene zuerst, „noch nie“ ans Ende
			la, lb := seen[a.ID], seen[b.ID]
			if (la == nil) != (lb == nil) {
				return la != nil
			}
			if la != nil && !la.Equal(*lb) {
				return la.After(*lb)
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		})
		for _, u := range all {
			if u.Online {
				data.Online = append(data.Online, u)
			} else if len(data.Offline) < presenceMaxIdle {
				data.Offline = append(data.Offline, u)
			} else {
				data.OfflineMore++
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	t, err := h.tmpl.Clone()
	if err == nil {
		err = bindLang(t, lang).ExecuteTemplate(w, "presence-list", data)
	}
	if err != nil {
		componentLog("anwesenheit").Warn().Err(err).Msg("liste nicht darstellbar")
	}
}
