package web

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Seitenvorlagen (0.90.0, Entwurf 12.1/12.2): wenige Vorlagen, die immer wieder
// verwendet werden; jede Seite der Bereiche gehört zu genau einer. Den Hinweis auf
// die Vorlage sehen nur Gestaltungs-Administratoren (system.ui_defaults), als
// Tooltip über dem Seitentitel mit „zuletzt angepasst von … am …“ (11.10).

type pageTemplate string

const (
	tplCockpit pageTemplate = "cockpit"
	tplListe   pageTemplate = "liste"
	tplDetail  pageTemplate = "detail"
	tplEinst   pageTemplate = "einstellungen"
	tplPlanung pageTemplate = "planung"
	tplBaum    pageTemplate = "baum"
	tplDienst  pageTemplate = "dienst"
	tplAktion  pageTemplate = "aktion"
	tplDruck   pageTemplate = "druck"
	tplTabelle pageTemplate = "tabelle"
)

var pageTemplateLabels = map[pageTemplate]string{
	tplCockpit: "Cockpit", tplListe: "Liste", tplDetail: "Detail", tplEinst: "Einstellungen",
	tplPlanung: "Planung", tplBaum: "Baum", tplDienst: "Dienst", tplAktion: "Aktion",
	tplDruck: "Druck", tplTabelle: "Tabelle/Zuordnung",
}

// pageTemplateLabel: Name der Vorlage für Anwender.
func pageTemplateLabel(t pageTemplate) string { return pageTemplateLabels[t] }

// pageTemplateOf: Vorlage je Menüeintrag (Schlüssel aus navAreaDefs). Was nicht
// besonders genannt ist, ist eine Liste.
var pageTemplateOf = func() map[string]pageTemplate {
	special := map[string]pageTemplate{
		"dashboard": tplCockpit, "leitstand": tplCockpit, "plantwatch": tplCockpit, "statistics": tplCockpit,
		"infrastructure": tplBaum, "storage": tplBaum, "orgchart": tplBaum,
		"shifts": tplPlanung, "calendar": tplPlanung,
		"import": tplTabelle, "export": tplTabelle, "sync": tplTabelle,
		"core": tplEinst, "serverconfig": tplEinst, "network": tplEinst, "backup": tplEinst, "roles": tplEinst,
		"nextcloud": tplDienst,
	}
	m := map[string]pageTemplate{}
	for _, a := range navAreaDefs {
		for _, k := range a.Items {
			if t, ok := special[k]; ok {
				m[k] = t
			} else {
				m[k] = tplListe
			}
		}
	}
	return m
}()

// keyUIMeta: wer eine Vorgabe der Firma zuletzt angepasst hat und wann, je Seite.
const keyUIMeta = "defaults.ui_meta"

type uiMeta struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// templateTip: Text des Tooltips über dem Seitentitel.
func templateTip(t pageTemplate, m uiMeta, hasMeta bool) string {
	s := "Vorlage: " + pageTemplateLabel(t)
	if hasMeta && strings.TrimSpace(m.By) != "" && !m.At.IsZero() {
		s += " · zuletzt angepasst von " + m.By + " am " + m.At.In(workLocation).Format("02.01.2006, 15:04")
	}
	return s
}

func decodeUIMeta(raw string) map[string]uiMeta {
	m := map[string]uiMeta{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}

// uiMetaFor: letzte Anpassung der Vorgabe einer Seite.
func (h *Handler) uiMetaFor(ctx context.Context, page string) (uiMeta, bool) {
	if h.db == nil {
		return uiMeta{}, false
	}
	m, ok := decodeUIMeta(h.appSetting(ctx, keyUIMeta, ""))[page]
	return m, ok
}

// setUIMeta: merkt, wer die Vorgabe einer Seite gerade angepasst hat.
func (h *Handler) setUIMeta(ctx context.Context, page, byName string) error {
	if h.db == nil {
		return nil
	}
	tx, err := h.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES ($1, '{}', NOW()) ON CONFLICT (key) DO NOTHING`, keyUIMeta); err != nil {
		return err
	}
	var raw string
	if err := tx.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1 FOR UPDATE`, keyUIMeta).Scan(&raw); err != nil {
		return err
	}
	m := decodeUIMeta(raw)
	m[page] = uiMeta{By: byName, At: time.Now().UTC()}
	out, _ := json.Marshal(m)
	if _, err := tx.Exec(ctx, `UPDATE app_settings SET value = $2, updated_at = NOW() WHERE key = $1`, keyUIMeta, string(out)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// templateTipFor: Tooltip für die aktuelle Seite (nur mit system.ui_defaults).
func (h *Handler) templateTipFor(ctx context.Context, b *BaseData) string {
	if b.navCan == nil || !b.navCan(permUIDefaults) {
		return ""
	}
	k := navKeyForPage(b.Page)
	t, ok := pageTemplateOf[k]
	if !ok {
		return ""
	}
	m, has := h.uiMetaFor(ctx, k)
	return templateTip(t, m, has)
}
