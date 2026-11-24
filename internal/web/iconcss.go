package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
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
const iconCSSBase = `.ti{display:inline-block;line-height:1}` +
	`.ti::before{content:"";display:inline-block;width:1em;height:1em;vertical-align:-.125em;background-color:currentColor;-webkit-mask:var(--ti) center/contain no-repeat;mask:var(--ti) center/contain no-repeat;-webkit-print-color-adjust:exact;print-color-adjust:exact}` +
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

// buildIconCSS erzeugt die Stildatei: Grundregeln, dann je Klasse eine Regel –
// eingebettet (embedded) oder als Adresse /vendor/<version>/icons/<name>.svg.
func buildIconCSS(set *tablericons.Set, embedded []string, version string) []byte {
	emb := make(map[string]bool, len(embedded))
	for _, n := range embedded {
		emb[n] = true
	}
	var b bytes.Buffer
	b.WriteString(iconCSSBase)
	for _, n := range set.Names() {
		if emb[n] {
			if svg, ok := set.SVG(n); ok {
				b.WriteString("." + n + `{--ti:url("` + iconDataURI(svg) + `")}` + "\n")
				continue
			}
		}
		b.WriteString("." + n + "{--ti:url(/vendor/" + version + "/icons/" + n + ".svg)}\n")
	}
	return b.Bytes()
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
	asset *staticAsset
	at    time.Time
	ok    bool // Auswahl aus der Datenbank gelesen (sonst nur kurz behalten)
}

const (
	iconCSSTTL    = 60 * time.Second
	iconCSSErrTTL = 5 * time.Second
)

var (
	iconCSSMu    sync.Mutex
	iconCSSCache *iconCSSEntry
	// iconCSSCurKey: Kennung der zuletzt gebauten Stildatei (fuer iconCSSHref).
	iconCSSCurKey atomic.Value
)

// resetIconCSSCache leert den Zwischenspeicher (nach Symbolwahl; Tests).
func resetIconCSSCache() {
	iconCSSMu.Lock()
	iconCSSCache = nil
	iconCSSMu.Unlock()
}

func (h *Handler) iconCSSEntry(ctx context.Context) *iconCSSEntry {
	iconCSSMu.Lock()
	defer iconCSSMu.Unlock()
	if e := iconCSSCache; e != nil {
		ttl := iconCSSTTL
		if !e.ok {
			ttl = iconCSSErrTTL
		}
		if time.Since(e.at) < ttl {
			return e
		}
	}
	names := usedIconNames()
	chosen, err := h.chosenIconNames(ctx)
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
	css := buildIconCSS(tablericons.Default(), names, pdh.Version())
	e := &iconCSSEntry{key: iconCSSKey(names), asset: newStaticAsset(css), at: time.Now(), ok: err == nil}
	iconCSSCache = e
	iconCSSCurKey.Store(e.key)
	return e
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

func noStore(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(code), code)
}

// IconCSSWeb: GET /vendor/{ver}/icons-{key}.css – oeffentlich. Fremde Version:
// 404; fremde Kennung der laufenden Version: die aktuelle Datei, nicht
// zwischenspeichern (sonst bliebe nach einer Symbolwahl ein leeres Kaestchen).
func (h *Handler) IconCSSWeb(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "ver") != pdh.Version() {
		noStore(w, http.StatusNotFound)
		return
	}
	e := h.iconCSSEntry(r.Context())
	cc := "public, max-age=31536000, immutable"
	if chi.URLParam(r, "key") != e.key {
		cc = "no-store"
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	serveAsset(w, r, "icons.css", e.asset, cc)
}

// IconSVGWeb: GET /vendor/{ver}/icons/{name}.svg – oeffentlich, ein Symbol.
func (h *Handler) IconSVGWeb(w http.ResponseWriter, r *http.Request) {
	if chi.URLParam(r, "ver") != pdh.Version() {
		noStore(w, http.StatusNotFound)
		return
	}
	svg, ok := tablericons.Default().SVG(chi.URLParam(r, "name"))
	if !ok {
		noStore(w, http.StatusNotFound)
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
