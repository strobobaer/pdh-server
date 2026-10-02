package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/faults"
)

// Datenblatt & Benutzerhandbuch fuer Ersatzteile: im Internet suchen (Claude
// mit Websuche, sonst DuckDuckGo), als PDF herunterladen und unter Dokumente
// ablegen. Die Herkunft wird gemerkt – "Aktualisieren" laedt erneut und
// ersetzt die Datei nur, wenn sich der Inhalt geaendert hat.

const partDocMaxBytes = 40 << 20

var partDocKinds = map[string]string{"datasheet": "Datenblatt", "manual": "Benutzerhandbuch"}

// Downloads groesserer PDFs brauchen laenger als die Bildsuche – gleicher
// abgesicherter Transport (kein Zugriff auf interne Netze), mehr Zeit.
var externalDocClient = &http.Client{
	Timeout:       90 * time.Second,
	Transport:     externalHTTPClient.Transport,
	CheckRedirect: externalHTTPClient.CheckRedirect,
}

type partDocHit struct {
	Kind     string `json:"kind"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Source   string `json:"source"`
	Official bool   `json:"official"`
	Language string `json:"language,omitempty"`
	Note     string `json:"note,omitempty"`
	Size     int64  `json:"size,omitempty"` // Bytes laut Server, 0 = unbekannt
}

// ── Suche ────────────────────────────────────────────────────

var (
	ddgResultRe  = regexp.MustCompile(`(?s)class="result__a" href="([^"]+)">(.*?)</a>`)
	ddgTagRe     = regexp.MustCompile(`<[^>]+>`)
	ddgPDFTypeRe = regexp.MustCompile(`(?s)<span class="result__type">\s*PDF\s*</span>`)
)

// parseDDGResults liest die HTML-Ergebnisseite von DuckDuckGo (nur PDFs).
func parseDDGResults(page, kind string) []partDocHit {
	var hits []partDocHit
	seen := map[string]bool{}
	for _, m := range ddgResultRe.FindAllStringSubmatch(page, -1) {
		href := html.UnescapeString(m[1])
		if strings.HasPrefix(href, "//") {
			href = "https:" + href
		}
		target := href
		if u, err := url.Parse(href); err == nil && u.Query().Get("uddg") != "" {
			target = u.Query().Get("uddg")
		}
		u, err := url.Parse(target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		isPDF := ddgPDFTypeRe.MatchString(m[2]) || strings.HasSuffix(strings.ToLower(u.Path), ".pdf")
		if !isPDF || seen[target] {
			continue
		}
		seen[target] = true
		title := strings.TrimSpace(html.UnescapeString(ddgTagRe.ReplaceAllString(ddgPDFTypeRe.ReplaceAllString(m[2], ""), "")))
		hits = append(hits, partDocHit{Kind: kind, Title: title, URL: target, Source: strings.TrimPrefix(u.Host, "www.")})
		if len(hits) >= 6 {
			break
		}
	}
	return hits
}

func searchDocsDDG(ctx context.Context, q string) ([]partDocHit, error) {
	queries := []struct{ kind, words string }{
		{"datasheet", "datasheet"},
		{"manual", "manual"},
	}
	var all []partDocHit
	for i, qq := range queries {
		if i > 0 {
			// DuckDuckGo sperrt schnelle Folgeanfragen
			select {
			case <-ctx.Done():
				return all, ctx.Err()
			case <-time.After(1500 * time.Millisecond):
			}
		}
		res, err := externalGet(ctx, "https://html.duckduckgo.com/html/?q="+url.QueryEscape(q+" "+qq.words+" filetype:pdf"), map[string]string{"Accept": "text/html"})
		if err != nil {
			return all, err
		}
		page, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
		res.Body.Close()
		// Sperrseite: kein Ergebnis, dafuer die "anomaly"-Abfrage
		if res.StatusCode != http.StatusOK || (!bytes.Contains(page, []byte("result__a")) && bytes.Contains(page, []byte("anomaly"))) {
			if len(all) > 0 {
				return all, nil
			}
			return nil, errors.New("DuckDuckGo hat die Suche vorübergehend gesperrt – in einer Minute erneut versuchen")
		}
		all = append(all, parseDDGResults(string(page), qq.kind)...)
	}
	return all, nil
}

// checkPDF prueft, ob hinter der Adresse wirklich ein PDF liegt (nur die
// ersten Bytes werden geladen) und liefert die Groesse, falls bekannt.
func checkPDF(ctx context.Context, raw string) (int64, bool) {
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	res, err := externalGet(cctx, raw, map[string]string{"Accept": "application/pdf,*/*", "Range": "bytes=0-2047"})
	if err != nil {
		return 0, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		return 0, false
	}
	head, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	if !isPDF(head) {
		return 0, false
	}
	size := res.ContentLength
	if cr := res.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			var n int64
			if _, err := fmt.Sscan(cr[i+1:], &n); err == nil {
				size = n
			}
		}
	} else if res.StatusCode == http.StatusPartialContent {
		size = 0
	}
	if size < 0 {
		size = 0
	}
	return size, true
}

// isPDF: PDF-Kennung am Anfang (einzelne Bytes davor sind erlaubt).
func isPDF(b []byte) bool {
	if len(b) > 1024 {
		b = b[:1024]
	}
	return bytes.Contains(b, []byte("%PDF-"))
}

// verifyDocHits behaelt nur erreichbare PDFs (parallel geprueft).
func verifyDocHits(ctx context.Context, hits []partDocHit) []partDocHit {
	if len(hits) > 14 {
		hits = hits[:14]
	}
	ok := make([]bool, len(hits))
	var wg sync.WaitGroup
	for i := range hits {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hits[i].Size, ok[i] = checkPDF(ctx, hits[i].URL)
		}(i)
	}
	wg.Wait()
	out := []partDocHit{}
	for i, h := range hits {
		if ok[i] {
			out = append(out, h)
		}
	}
	return out
}

// PartDocSearchWeb: GET /inventory/{id}/doc-search?q= – Treffer als JSON.
func (h *Handler) PartDocSearchWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		chatError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	id := chi.URLParam(r, "id")
	// unter dem Zeitlimit gaengiger Reverse-Proxys (Cloudflare: 100 s)
	ctx, cancel := context.WithTimeout(r.Context(), 95*time.Second)
	defer cancel()
	p, err := h.loadPartMaster(ctx, id)
	if err != nil {
		chatError(w, http.StatusNotFound, "Ersatzteil nicht gefunden")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = partDocDefaultQuery(p)
	}
	manufacturer := p.ManufacturerName
	if manufacturer == "" {
		manufacturer = p.ManufacturerLegacy
	}
	var hits []partDocHit
	source, note := "", ""
	cHits, cErr := h.faults.FindPartDocuments(ctx, faults.PartDocQuery{
		Name: p.Name, PartNumber: p.PartNumber, Manufacturer: manufacturer, ManufacturerPart: p.ManufacturerPart, Extra: r.URL.Query().Get("q"),
	})
	if cErr == nil {
		for _, c := range cHits {
			hits = append(hits, partDocHit{Kind: c.Kind, Title: c.Title, URL: c.URL, Source: c.Source, Official: c.Official, Language: c.Language, Note: c.Note})
		}
		hits = verifyDocHits(ctx, hits)
		source = "Claude-Websuche"
	}
	if len(hits) == 0 {
		if cErr != nil && !errors.Is(cErr, faults.ErrNoWebSearch) {
			componentLog("teiledokumente").Warn().Err(cErr).Str("teil", id).Msg("claude-suche fehlgeschlagen")
			note = "Claude-Suche nicht möglich (" + cErr.Error() + ") – Ergebnisse von DuckDuckGo."
		}
		d, dErr := searchDocsDDG(ctx, q)
		hits = verifyDocHits(ctx, d)
		source = "DuckDuckGo"
		if dErr != nil && len(hits) == 0 {
			msg := "Dokumentsuche nicht möglich: " + dErr.Error()
			if note != "" {
				msg = note + " " + msg
			}
			chatError(w, http.StatusBadGateway, msg)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": hits, "source": source, "note": note, "query": q})
}

func partDocDefaultQuery(p PartMaster) string {
	m := p.ManufacturerName
	if m == "" {
		m = p.ManufacturerLegacy
	}
	if p.ManufacturerPart != "" {
		return strings.TrimSpace(m + " " + p.ManufacturerPart)
	}
	return strings.TrimSpace(m + " " + p.Name)
}

// ── Herunterladen & ablegen ──────────────────────────────────

// downloadPDF laedt eine PDF-Datei ueber den abgesicherten Client.
func downloadPDF(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", errors.New("ungültige Adresse")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/pdf,*/*")
	res, err := externalDocClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Dokument konnte nicht geladen werden: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Dokument konnte nicht geladen werden (HTTP %d)", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, partDocMaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("Dokument konnte nicht geladen werden: %w", err)
	}
	if len(data) > partDocMaxBytes {
		return nil, "", fmt.Errorf("Dokument ist größer als %d MB", partDocMaxBytes>>20)
	}
	if !isPDF(data) {
		return nil, "", errors.New("Unter der Adresse liegt kein PDF (evtl. Anmeldung oder Download-Seite des Anbieters)")
	}
	return data, path.Base(res.Request.URL.Path), nil
}

var unsafeFileChars = regexp.MustCompile(`[^\p{L}\p{N}._ ()+-]+`)

// partDocFilename: lesbarer Dateiname, immer mit .pdf.
func partDocFilename(base, title, kind string) string {
	name := base
	if name == "" || name == "." || name == "/" || !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name = title
		if name == "" {
			name = partDocKinds[kind]
		}
		name += ".pdf"
	}
	if dec, err := url.PathUnescape(name); err == nil {
		name = dec
	}
	name = strings.TrimSpace(unsafeFileChars.ReplaceAllString(name, "_"))
	if r := []rune(name); len(r) > 120 {
		name = string(r[:116]) + ".pdf"
	}
	return name
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (h *Handler) partDocHistory(ctx context.Context, partID, kind, msg, value, userID string) {
	_, _ = h.db.Exec(ctx, `
		INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
		VALUES ('spare_part', $1::uuid, 'update', $2, $3, $4, $5)`,
		partID, partDocKinds[kind], value, nullID(userID), msg)
}

// PartDocTakeWeb: POST /inventory/{id}/documents (url, kind, title) – PDF
// herunterladen und als Dokument des Teils ablegen.
func (h *Handler) PartDocTakeWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		chatError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	id := chi.URLParam(r, "id")
	r.ParseForm()
	raw := strings.TrimSpace(r.FormValue("url"))
	kind := r.FormValue("kind")
	if _, ok := partDocKinds[kind]; !ok {
		chatError(w, http.StatusBadRequest, "Bitte Datenblatt oder Benutzerhandbuch wählen")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 95*time.Second)
	defer cancel()
	if _, err := h.loadPartMaster(ctx, id); err != nil {
		chatError(w, http.StatusNotFound, "Ersatzteil nicht gefunden")
		return
	}
	// dieselbe Quelle schon vorhanden? Dann nur aktualisieren.
	var existing string
	_ = h.db.QueryRow(ctx, `SELECT id::text FROM attachments WHERE ref_type = 'inventory' AND ref_id = $1::uuid AND source_url = $2 LIMIT 1`, id, raw).Scan(&existing)
	if existing != "" {
		msg, err := h.refreshPartDoc(ctx, id, existing, getUser(r).ID)
		if err != nil {
			chatError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Schon vorhanden – " + msg})
		return
	}
	data, base, err := downloadPDF(ctx, raw)
	if err != nil {
		chatError(w, http.StatusBadGateway, err.Error())
		return
	}
	name := partDocFilename(base, strings.TrimSpace(r.FormValue("title")), kind)
	rel, err := writePartDoc(id, data)
	if err != nil {
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	u := getUser(r)
	if _, err := h.db.Exec(ctx, `
		INSERT INTO attachments (id, ref_type, ref_id, filename, filepath, mimetype, size_bytes, caption, created_by,
			doc_kind, source_url, source_sha256, source_checked_at)
		VALUES (gen_random_uuid(), 'inventory', $1::uuid, $2, $3, 'application/pdf', $4, $5, $6, $7, $8, $9, NOW())`,
		id, name, rel, len(data), partDocKinds[kind], u.ID, kind, raw, sha256Hex(data)); err != nil {
		_ = os.Remove(filepath.Join("uploads", filepath.FromSlash(rel)))
		chatError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.partDocHistory(ctx, id, kind, partDocKinds[kind]+" aus dem Internet übernommen", raw, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": partDocKinds[kind] + " unter Dokumente abgelegt"})
}

// writePartDoc speichert die Datei unter uploads/inventory/<teil>/.
func writePartDoc(partID string, data []byte) (string, error) {
	rel := filepath.Join("inventory", partID, "doc-"+randomHex(8)+".pdf")
	abs := filepath.Join("uploads", rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// refreshPartDoc laedt ein Dokument erneut von seiner Quelle. Hat sich der
// Inhalt geaendert, wird die Datei ersetzt (die alte entfernt).
func (h *Handler) refreshPartDoc(ctx context.Context, partID, attID, userID string) (string, error) {
	var src, oldRel, oldSum, kind, name string
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(source_url,''), filepath, COALESCE(source_sha256,''), COALESCE(doc_kind,''), filename
		FROM attachments WHERE id = $1::uuid AND ref_type = 'inventory' AND ref_id = $2::uuid`, attID, partID).
		Scan(&src, &oldRel, &oldSum, &kind, &name)
	if err != nil {
		return "", errors.New("Dokument nicht gefunden")
	}
	if src == "" {
		return "", errors.New("Für dieses Dokument ist keine Quelle bekannt")
	}
	data, _, err := downloadPDF(ctx, src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	sum := sha256Hex(data)
	if sum == oldSum {
		_, _ = h.db.Exec(ctx, `UPDATE attachments SET source_checked_at = NOW() WHERE id = $1::uuid`, attID)
		return name + " ist aktuell (unverändert)", nil
	}
	rel, err := writePartDoc(partID, data)
	if err != nil {
		return "", err
	}
	if _, err := h.db.Exec(ctx, `
		UPDATE attachments SET filepath = $1, size_bytes = $2, source_sha256 = $3, source_checked_at = NOW()
		WHERE id = $4::uuid`, rel, len(data), sum, attID); err != nil {
		_ = os.Remove(filepath.Join("uploads", filepath.FromSlash(rel)))
		return "", err
	}
	_ = os.Remove(filepath.Join("uploads", filepath.FromSlash(oldRel)))
	label := partDocKinds[kind]
	if label == "" {
		label = "Dokument"
	}
	h.partDocHistory(ctx, partID, kind, label+" aktualisiert (neue Fassung beim Anbieter)", src, userID)
	return name + " aktualisiert – neue Fassung übernommen", nil
}

// PartDocRefreshWeb: POST /inventory/{id}/documents/{att}/refresh – ein
// Dokument oder mit att=all alle Dokumente mit bekannter Quelle neu laden.
func (h *Handler) PartDocRefreshWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		chatError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	id, att := chi.URLParam(r, "id"), chi.URLParam(r, "att")
	ctx, cancel := context.WithTimeout(r.Context(), 95*time.Second)
	defer cancel()
	ids := []string{att}
	if att == "all" {
		ids = nil
		rows, err := h.db.Query(ctx, `SELECT id::text FROM attachments WHERE ref_type = 'inventory' AND ref_id = $1::uuid AND source_url IS NOT NULL ORDER BY created_at`, id)
		if err != nil {
			chatError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for rows.Next() {
			var s string
			if rows.Scan(&s) == nil {
				ids = append(ids, s)
			}
		}
		rows.Close()
		if len(ids) == 0 {
			chatError(w, http.StatusBadRequest, "Keine Dokumente aus dem Internet vorhanden")
			return
		}
	} else if !uuidInPathRe.MatchString(att) {
		chatError(w, http.StatusBadRequest, "Dokument nicht gefunden")
		return
	}
	var msgs, errs []string
	for _, a := range ids {
		m, err := h.refreshPartDoc(ctx, id, a, getUser(r).ID)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		msgs = append(msgs, m)
	}
	if len(msgs) == 0 {
		chatError(w, http.StatusBadGateway, strings.Join(errs, " · "))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": strings.Join(append(msgs, errs...), " · "), "failed": len(errs)})
}
