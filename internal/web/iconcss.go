package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh"
	"pdh/internal/web/tablericons"
)

// Symbol-Stildatei: statt der 1,4 MB grossen Symbolschrift liefert der Server
// eine Stildatei, in der die genutzten Symbole als SVG eingebettet sind (sofort
// da, kein Nachladen) und alle uebrigen als Adresse (erst beim Anzeigen geladen).
// Die Symbole werden per CSS-Maske in Textfarbe gezeichnet; die Vorlagen
// behalten ihr <i class="ti ti-…">.

//go:embed icons_used.txt
var iconsUsedTxt string

// iconCSSBase: Grundregeln fuer alle Symbole.
const iconCSSBase = `.ti{display:inline-block;line-height:1}.ti[hidden]{display:none}` +
	`.ti::before{content:"";display:inline-block;width:1em;height:1em;vertical-align:-.125em;background-color:currentColor;-webkit-mask:var(--ti,linear-gradient(transparent,transparent)) center/contain no-repeat;mask:var(--ti,linear-gradient(transparent,transparent)) center/contain no-repeat;-webkit-print-color-adjust:exact;print-color-adjust:exact}` +
	`@media (forced-colors:active){.ti::before{background-color:CanvasText}}` + "\n"

var (
	usedOnce  sync.Once
	usedNames []string
)

// usedIconNames: alle vom PDH genutzten Klassen – icons_used.txt (aus Vorlagen,
// Go-Code und Skripten) plus die Vorgaben aller Symbol-Schluessel (iconGroups,
// darin die Navigation aus navDefs). Sortiert, nur bekannte Klassen.
func usedIconNames() []string {
	usedOnce.Do(func() {
		set := tablericons.Default()
		seen := map[string]bool{}
		add := func(n string) {
			n = strings.TrimSpace(n)
			if n != "" && !seen[n] && set.Has(n) {
				seen[n] = true
				usedNames = append(usedNames, n)
			}
		}
		for _, ln := range strings.Split(iconsUsedTxt, "\n") {
			add(ln)
		}
		for _, g := range iconGroups() {
			for _, it := range g.Items {
				add(it.Default)
			}
		}
		sort.Strings(usedNames)
	})
	return append([]string(nil), usedNames...)
}

// iconDataURI: SVG als URL-kodierte data-Adresse (in url("…") mit doppelten
// Anfuehrungszeichen, daher im SVG nur einfache).
func iconDataURI(svg []byte) string {
	s := strings.ReplaceAll(string(svg), `"`, `'`)
	r := strings.NewReplacer("%", "%25", "<", "%3C", ">", "%3E", "#", "%23", "\n", "%20", "\r", "")
	return "data:image/svg+xml," + r.Replace(s)
}

// buildIconCSS erzeugt die beiden Stildateien: die Hauptdatei (Grundregeln und
// die eingebetteten Symbole, blockiert das Zeichnen) und die Rest-Datei (je
// uebrigem Symbol eine Adress-Regel /vendor/<version>/icons/<name>.svg, wird
// erst nach dem Laden der Seite geholt).
func buildIconCSS(set *tablericons.Set, embedded []string, version string) (mainCSS, restCSS []byte) {
	emb := make(map[string]bool, len(embedded))
	for _, n := range embedded {
		emb[n] = true
	}
	var b, rest bytes.Buffer
	b.WriteString(iconCSSBase)
	for _, n := range set.Names() {
		if emb[n] {
			if svg, ok := set.SVG(n); ok {
				b.WriteString("." + n + `{--ti:url("` + iconDataURI(svg) + `")}` + "\n")
				continue
			}
		}
		rest.WriteString("." + n + "{--ti:url(/vendor/" + version + "/icons/" + n + ".svg)}\n")
	}
	return b.Bytes(), rest.Bytes()
}

// iconCSSKey: kurze Kennung der eingebetteten Namen (Teil der Adresse).
func iconCSSKey(embedded []string) string {
	sum := sha256.Sum256([]byte(strings.Join(embedded, "\n")))
	return hex.EncodeToString(sum[:])[:10]
}

// chosenIconNames: vom Anwender gewaehlte Klassen – Aenderungen unter
// /admin/icons (app_settings), Kategorien, Statistik-Kacheln, Anlagen-Beobachtung
// und IT-Kategorien. Nur gueltige Klassen.
func (h *Handler) chosenIconNames(ctx context.Context) ([]string, error) {
	if h == nil || h.db == nil {
		return nil, nil
	}
	set := tablericons.Default()
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n != "" && !seen[n] && set.Has(n) {
			seen[n] = true
		}
	}
	var raw string
	if err := h.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key = $1`, KeyIcons).Scan(&raw); err == nil {
		m := map[string]string{}
		if json.Unmarshal([]byte(raw), &m) == nil {
			for _, v := range m {
				add(v)
			}
		}
	}
	for _, q := range []string{
		`SELECT icon FROM categories`,
		`SELECT icon FROM grafana_dashboards`,
		`SELECT icon FROM plant_watch_boards`,
		`SELECT icon FROM it_asset_categories`,
	} {
		rows, err := h.db.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err == nil {
				add(s)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// iconCSSEntry: fertige Stildatei, einmal gzip-komprimiert.
type iconCSSEntry struct {
	key   string
	asset *staticAsset // Hauptdatei
	rest  *staticAsset // Rest-Datei (Adress-Regeln der uebrigen Symbole)
	at    time.Time
	ok    bool // Auswahl aus der Datenbank gelesen (sonst nur kurz behalten)
}

const (
	iconCSSTTL    = 60 * time.Second
	iconCSSErrTTL = 5 * time.Second
)

var (
	iconCSSMu    sync.Mutex // schuetzt iconCSSCache (nie ueber Datenbank oder gzip gehalten)
	iconCSSCache *iconCSSEntry
	// iconCSSBuildMu: ein Neubau zur Zeit (Datenbankabfrage und gzip ausserhalb von iconCSSMu).
	iconCSSBuildMu sync.Mutex
	// iconCSSCurKey: Kennung der zuletzt gebauten Stildatei (fuer iconCSSHref).
	iconCSSCurKey atomic.Value
)

// resetIconCSSCache leert den Zwischenspeicher (Tests).
func resetIconCSSCache() {
	iconCSSMu.Lock()
	iconCSSCache = nil
	iconCSSMu.Unlock()
}

// iconCSSFresh: der gespeicherte Eintrag, solange er noch gilt (sonst nil).
func iconCSSFresh() *iconCSSEntry {
	iconCSSMu.Lock()
	defer iconCSSMu.Unlock()
	e := iconCSSCache
	if e == nil {
		return nil
	}
	ttl := iconCSSTTL
	if !e.ok {
		ttl = iconCSSErrTTL
	}
	if time.Since(e.at) < ttl {
		return e
	}
	return nil
}

// iconCSSEntry liefert den Eintrag; ist er abgelaufen, wird neu gelesen. Die
// Abfragen laufen mit eigenem, kurzem Zeitlimit (nicht im Kontext der Anfrage).
func (h *Handler) iconCSSEntry(_ context.Context) *iconCSSEntry {
	if e := iconCSSFresh(); e != nil {
		return e
	}
	return h.rebuildIconCSS(false)
}

// rebuildIconCSS liest die gewaehlten Symbole und baut die Stildatei nur dann neu,
// wenn sich die Kennung geaendert hat; sonst wird nur der Zeitstempel erneuert.
// Ein Lesefehler behaelt die bisherige Datei (und versucht es nach 5 s wieder).
func (h *Handler) rebuildIconCSS(force bool) *iconCSSEntry {
	iconCSSBuildMu.Lock()
	defer iconCSSBuildMu.Unlock()
	if !force {
		if e := iconCSSFresh(); e != nil {
			return e
		}
	}
	iconCSSMu.Lock()
	old := iconCSSCache
	iconCSSMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	chosen, err := h.chosenIconNames(ctx)
	cancel()
	if err != nil && old != nil {
		e := &iconCSSEntry{key: old.key, asset: old.asset, rest: old.rest, at: time.Now(), ok: false}
		iconCSSMu.Lock()
		iconCSSCache = e
		iconCSSMu.Unlock()
		return e
	}
	names := usedIconNames()
	if err == nil {
		seen := map[string]bool{}
		for _, n := range names {
			seen[n] = true
		}
		for _, n := range chosen {
			if !seen[n] {
				names = append(names, n)
			}
		}
		sort.Strings(names)
	}
	key := iconCSSKey(names)
	e := &iconCSSEntry{key: key, at: time.Now(), ok: err == nil}
	if old != nil && old.key == key {
		e.asset, e.rest = old.asset, old.rest
	} else {
		mainCSS, restCSS := buildIconCSS(tablericons.Default(), names, pdh.Version())
		e.asset, e.rest = newStaticAsset(mainCSS), newStaticAsset(restCSS)
	}
	iconCSSMu.Lock()
	iconCSSCache = e
	iconCSSMu.Unlock()
	iconCSSCurKey.Store(e.key)
	return e
}

// iconChoicesChanged: nach jedem Speichern oder Loeschen eines gewaehlten Symbols
// (Symbol-Verwaltung, Kategorien, Grafana-Kacheln, Anlagen-Beobachtung, IT-Kategorien)
// sofort neu lesen, damit das Symbol eingebettet wird und sich die Adresse aendert.
func (h *Handler) iconChoicesChanged() { h.rebuildIconCSS(true) }

// refreshIconCSS: Netz unter den Speicherwegen (andere Server des Verbunds,
// Eintraege per Datenbank) – baut nur bei geaenderter Kennung neu.
func (h *Handler) refreshIconCSS() { h.rebuildIconCSS(true) }

// StartIconCSSRefresh prueft jede Minute, ob sich die gewaehlten Symbole geaendert
// haben; endet mit ctx.
func (h *Handler) StartIconCSSRefresh(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	go func() {
		t := time.NewTicker(iconCSSTTL)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.refreshIconCSS()
			}
		}
	}()
}

// iconCSS liefert die Stildatei und ihre Kennung; hoechstens 60 s alt.
func (h *Handler) iconCSS(ctx context.Context) (css []byte, key string) {
	e := h.iconCSSEntry(ctx)
	return e.asset.raw, e.key
}

// iconCSSHref: Adresse der Stildatei fuer die Vorlagen (/vendor/<Version>/icons-<key>.css).
func iconCSSHref() string {
	key, _ := iconCSSCurKey.Load().(string)
	if key == "" {
		key = iconCSSKey(usedIconNames())
	}
	return "/vendor/" + pdh.Version() + "/icons-" + key + ".css"
}

// iconCSSRestHref: Adresse der Rest-Datei (/vendor/<Version>/icons-rest-<key>.css).
func iconCSSRestHref() string {
	key, _ := iconCSSCurKey.Load().(string)
	if key == "" {
		key = iconCSSKey(usedIconNames())
	}
	return "/vendor/" + pdh.Version() + "/icons-rest-" + key + ".css"
}

// iconCSSLinks: Symbol-Stildateien fuer den Seitenkopf. Die Hauptdatei blockiert
// das Zeichnen (nur Grundregeln und eingebettete Symbole); die Rest-Datei haengt
// ein kleines Skript erst nach dem load-Ereignis an, damit sie weder das Zeichnen
// noch load verzoegert. Ohne Skript laedt sie ueber <noscript>.
func iconCSSLinks() template.HTML {
	main := html.EscapeString(iconCSSHref())
	rest := html.EscapeString(iconCSSRestHref())
	js, _ := json.Marshal(rest)
	return template.HTML(`<link rel="stylesheet" href="` + main + `">` +
		`<script>(function(){function f(){var l=document.createElement("link");l.rel="stylesheet";l.href=` + string(js) +
		`;document.head.appendChild(l)}if(document.readyState==="complete")f();else addEventListener("load",f)})()</script>` +
		`<noscript><link rel="stylesheet" href="` + rest + `"></noscript>`)
}

func noStore(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(code), code)
}

// redirectNoStore: Weiterleitung auf die Adresse der laufenden Version, nie zwischengespeichert.
func redirectNoStore(w http.ResponseWriter, r *http.Request, to string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusFound)
}

// IconCSSWeb: GET /vendor/{ver}/icons-{key}.css – oeffentlich. Fremde Version:
// Weiterleitung auf die aktuelle Stildatei (Seiten, die ueber ein Update offen
// blieben); fremde Kennung der laufenden Version: die aktuelle Datei, nicht
// zwischenspeichern (sonst bliebe nach einer Symbolwahl ein leeres Kaestchen).
func (h *Handler) IconCSSWeb(w http.ResponseWriter, r *http.Request) {
	e := h.iconCSSEntry(r.Context())
	if chi.URLParam(r, "ver") != pdh.Version() {
		redirectNoStore(w, r, iconCSSHref())
		return
	}
	cc := "public, max-age=31536000, immutable"
	if chi.URLParam(r, "key") != e.key {
		cc = "no-store"
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	serveAsset(w, r, "icons.css", e.asset, cc)
}

// iconKeyRe: gueltige Kennung (10 Hexzeichen).
var iconKeyRe = regexp.MustCompile(`^[0-9a-f]{10}$`)

// IconCSSRestWeb: GET /vendor/{ver}/icons-rest-{key}.css – wie IconCSSWeb, aber
// die Rest-Datei. Ungueltige Kennung: 404.
func (h *Handler) IconCSSRestWeb(w http.ResponseWriter, r *http.Request) {
	if !iconKeyRe.MatchString(chi.URLParam(r, "key")) {
		noStore(w, http.StatusNotFound)
		return
	}
	e := h.iconCSSEntry(r.Context())
	if chi.URLParam(r, "ver") != pdh.Version() {
		redirectNoStore(w, r, iconCSSRestHref())
		return
	}
	cc := "public, max-age=31536000, immutable"
	if chi.URLParam(r, "key") != e.key {
		cc = "no-store"
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	serveAsset(w, r, "icons-rest.css", e.rest, cc)
}

// IconSVGWeb: GET /vendor/{ver}/icons/{name}.svg – oeffentlich, ein Symbol.
func (h *Handler) IconSVGWeb(w http.ResponseWriter, r *http.Request) {
	svg, ok := tablericons.Default().SVG(chi.URLParam(r, "name"))
	if !ok {
		noStore(w, http.StatusNotFound)
		return
	}
	if chi.URLParam(r, "ver") != pdh.Version() {
		redirectNoStore(w, r, "/vendor/"+pdh.Version()+"/icons/"+chi.URLParam(r, "name")+".svg")
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	serveAsset(w, r, "icon.svg", plainAsset(svg), "public, max-age=31536000, immutable")
}

// LegacyIconVendorWeb: alter Pfad /vendor/tabler-icons/*. Nur die frueher
// verlinkte tabler-icons.min.css (beliebiges ?v=) bleibt erreichbar – als
// Weiterleitung auf die aktuelle Stildatei, damit alte Seiten im
// Zwischenspeicher weiter Symbole zeigen. Alles andere: 404.
func LegacyIconVendorWeb(w http.ResponseWriter, r *http.Request) {
	if strings.TrimPrefix(r.URL.Path, "/vendor/tabler-icons/") == "tabler-icons.min.css" {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, iconCSSHref(), http.StatusFound)
		return
	}
	noStore(w, http.StatusNotFound)
}
