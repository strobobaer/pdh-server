package web

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Eine Abfrage statt vieler: baseData braucht bei jedem Seitenaufruf mehrere
// Spalten derselben users-Zeile (Darstellung, Profilbild, Navigation,
// Live-Übersetzung, Broker). userRow liest sie einmal und legt sie in den
// Request-Kontext; die Hilfsfunktionen nehmen sie von dort und fragen nur
// ohne diesen Vorrat (andere Aufrufer) selbst nach.

type userRow struct {
	id                      string
	palette, font, style    string
	scale, row              int
	avatarPath              *string
	navLayout               []byte
	liveTranslate           bool
	translateLang, language string
	broker                  [4]bool // faults, tickets, tasks, maintenance
	active                  bool
	ListSort                map[string]string // gemerkte Sortierwahl je Listenart (users.list_sort)
	uiFrame                 string            // Rahmen: '' wie Firma, classic, new (users.ui_frame, Migration 226)
	uiAreas                 []byte            // Reihenfolge und Favoriten der Bereiche (users.ui_areas)
}

type userRowKey struct{}

// withUserRow liest die Zeile (eine Abfrage) und haengt sie an den Kontext.
func (h *Handler) withUserRow(ctx context.Context, uid string) context.Context {
	if h.db == nil || uid == "" {
		return ctx
	}
	u := &userRow{id: uid}
	var listSort []byte
	err := h.db.QueryRow(ctx, `SELECT ui_palette, ui_font, ui_style, ui_scale, ui_row, avatar_path, nav_layout,
			live_translate, translate_lang, language, broker_faults, broker_tickets, broker_tasks, broker_maintenance, active, list_sort,
			ui_frame, ui_areas
		FROM users WHERE id = $1::uuid`, uid).
		Scan(&u.palette, &u.font, &u.style, &u.scale, &u.row, &u.avatarPath, &u.navLayout,
			&u.liveTranslate, &u.translateLang, &u.language, &u.broker[0], &u.broker[1], &u.broker[2], &u.broker[3], &u.active, &listSort,
			&u.uiFrame, &u.uiAreas)
	if err != nil {
		return ctx // Hilfsfunktionen fragen dann wie bisher einzeln
	}
	if len(listSort) > 0 {
		_ = json.Unmarshal(listSort, &u.ListSort) // kaputter Wert = keine gemerkte Wahl
	}
	return context.WithValue(ctx, userRowKey{}, u)
}

// userRowFrom: Vorrat aus dem Kontext, sofern er zu dieser Person gehoert.
func userRowFrom(ctx context.Context, uid string) *userRow {
	u, _ := ctx.Value(userRowKey{}).(*userRow)
	if u == nil || u.id != uid {
		return nil
	}
	return u
}

// ── Push-Schalter: bei jedem Seitenaufruf gebraucht, aendert sich selten ──

var pushFlagCache struct {
	sync.Mutex
	on bool
	at time.Time
}

const pushFlagTTL = 15 * time.Second

func pushFlagCached() (on, ok bool) {
	pushFlagCache.Lock()
	defer pushFlagCache.Unlock()
	return pushFlagCache.on, time.Since(pushFlagCache.at) < pushFlagTTL
}

func pushFlagStore(on bool) {
	pushFlagCache.Lock()
	pushFlagCache.on, pushFlagCache.at = on, time.Now()
	pushFlagCache.Unlock()
}
