package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
)

// Bild-Picker fuer den Ersatzteilstamm: Internet-Bildsuche (DuckDuckGo,
// Rueckfall Openverse), Uebernahme per Klick/URL/Upload. Das Bild wird
// lokal gespeichert (Anhang des Teils) und als Teilebild gesetzt, statt
// es beim Anbieter einzubinden.

const partImageMaxBytes = 8 << 20

var partImageTypes = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
}

type imageHit struct {
	Image     string `json:"image"`
	Thumbnail string `json:"thumbnail"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// ── Sicherer HTTP-Client (kein Zugriff auf interne Netze) ────

func blockedIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64) // 100.64.0.0/10 (CGNAT)
}

// externalHTTPClient prueft jede (auch umgeleitete) Verbindung auf die
// tatsaechlich angesprochene IP - verhindert Abrufe interner Dienste (SSRF).
var externalHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: 8 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				if blockedIP(net.ParseIP(host)) {
					return fmt.Errorf("Adresse %s ist nicht erlaubt", host)
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          10,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("zu viele Weiterleitungen")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errors.New("nur http/https erlaubt")
		}
		return nil
	},
}

// Schlichter, ehrlicher User-Agent: ein vollstaendiger Browser-UA passt nicht zum
// TLS-Fingerabdruck des Go-Clients und wird von DuckDuckGo abgewiesen.
const browserUA = "Mozilla/5.0 (compatible; PDH-Server/1.0; Ersatzteil-Bildsuche)"

func externalGet(ctx context.Context, rawURL string, headers map[string]string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("ungültige Adresse")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return externalHTTPClient.Do(req)
}

// ── Bildsuche ────────────────────────────────────────────────

var ddgVqdRe = regexp.MustCompile(`vqd=["']?([0-9-]+)`)

func searchImagesDDG(ctx context.Context, q string) ([]imageHit, error) {
	res, err := externalGet(ctx, "https://duckduckgo.com/?iax=images&ia=images&q="+url.QueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	page, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	m := ddgVqdRe.FindSubmatch(page)
	if m == nil {
		return nil, errors.New("DuckDuckGo: kein Suchtoken")
	}
	res, err = externalGet(ctx, "https://duckduckgo.com/i.js?l=de-de&o=json&f=,,,,,&p=1&q="+url.QueryEscape(q)+"&vqd="+string(m[1]),
		map[string]string{"Referer": "https://duckduckgo.com/", "Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DuckDuckGo: HTTP %d", res.StatusCode)
	}
	var payload struct {
		Results []struct {
			Image     string `json:"image"`
			Thumbnail string `json:"thumbnail"`
			Title     string `json:"title"`
			URL       string `json:"url"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	var hits []imageHit
	for _, r := range payload.Results {
		if r.Image == "" || r.Thumbnail == "" {
			continue
		}
		src := r.URL
		if u, err := url.Parse(r.URL); err == nil {
			src = u.Host
		}
		hits = append(hits, imageHit{Image: r.Image, Thumbnail: r.Thumbnail, Title: r.Title, Source: src, Width: r.Width, Height: r.Height})
		if len(hits) >= 40 {
			break
		}
	}
	return hits, nil
}

func searchImagesOpenverse(ctx context.Context, q string) ([]imageHit, error) {
	res, err := externalGet(ctx, "https://api.openverse.org/v1/images/?page_size=20&q="+url.QueryEscape(q), map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Openverse: HTTP %d", res.StatusCode)
	}
	var payload struct {
		Results []struct {
			URL       string `json:"url"`
			Thumbnail string `json:"thumbnail"`
			Title     string `json:"title"`
			Provider  string `json:"provider"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	var hits []imageHit
	for _, r := range payload.Results {
		if r.URL == "" {
			continue
		}
		thumb := r.Thumbnail
		if thumb == "" {
			thumb = r.URL
		}
		hits = append(hits, imageHit{Image: r.URL, Thumbnail: thumb, Title: r.Title, Source: r.Provider, Width: r.Width, Height: r.Height})
	}
	return hits, nil
}

// PartImageSearchWeb liefert Bildtreffer als JSON.
func (h *Handler) PartImageSearchWeb(w http.ResponseWriter, r *http.Request) {
	if !h.canEditParts(r) {
		chatError(w, http.StatusForbidden, "keine Berechtigung")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(q)) < 2 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"results": []imageHit{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	hits, err := searchImagesDDG(ctx, q)
	source := "DuckDuckGo"
	if err != nil || len(hits) == 0 {
		var err2 error
		hits, err2 = searchImagesOpenverse(ctx, q)
		source = "Openverse"
		if err2 != nil && err != nil {
			chatError(w, http.StatusBadGateway, "Bildsuche nicht erreichbar: "+err.Error())
			return
		}
	}
	if hits == nil {
		hits = []imageHit{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"results": hits, "source": source})
}

// ── Uebernahme ───────────────────────────────────────────────

// readImage prueft Groesse und Typ (anhand des Inhalts, nicht der Endung).
func readImage(src io.Reader) ([]byte, string, error) {
	data, err := io.ReadAll(io.LimitReader(src, partImageMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > partImageMaxBytes {
		return nil, "", fmt.Errorf("Bild ist größer als %d MB", partImageMaxBytes>>20)
	}
	mt := http.DetectContentType(data)
	ext, ok := partImageTypes[mt]
	if !ok {
		return nil, "", fmt.Errorf("kein unterstütztes Bildformat (%s)", mt)
	}
	return data, ext, nil
}

// PartImageSetWeb uebernimmt ein Bild (URL aus der Suche/eingefuegt oder
// Datei-Upload), speichert es als Anhang und setzt es als Teilebild.
// action=remove entfernt nur die Zuordnung (der Anhang bleibt).
func (h *Handler) PartImageSetWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.canEditParts(r) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, partImageMaxBytes+(1<<20))
	_ = r.ParseMultipartForm(partImageMaxBytes + (1 << 20))
	back := r.FormValue("tab")
	if back == "" {
		back = "master"
	}
	old, err := h.loadPartMaster(ctx, id)
	if err != nil {
		partRedirect(w, r, id, back, "", err)
		return
	}
	setImage := func(imageURL string) error {
		desc := joinPartImage(old.Description, imageURL)
		if _, err := h.db.Exec(ctx, `UPDATE spare_parts SET description = $1, updated_at = NOW() WHERE id = $2::uuid`, desc, id); err != nil {
			return err
		}
		_, _ = h.db.Exec(ctx, `
			INSERT INTO record_history (ref_type, ref_id, action, field_name, old_value, new_value, created_by, message)
			VALUES ('spare_part', $1::uuid, 'update', 'Teilebild', $2, $3, $4, 'Teilebild geändert')`,
			id, old.ImageURL, imageURL, nullID(getUser(r).ID))
		return nil
	}
	if r.FormValue("action") == "remove" {
		partRedirect(w, r, id, back, "Teilebild entfernt", setImage(""))
		return
	}

	var data []byte
	var ext, name string
	if file, fh, ferr := r.FormFile("file"); ferr == nil {
		defer file.Close()
		data, ext, err = readImage(file)
		name = filepath.Base(fh.Filename)
	} else if raw := strings.TrimSpace(r.FormValue("url")); raw != "" {
		data, ext, name, err = downloadImage(ctx, raw)
		// Rueckfall: Originalbild gesperrt (Hotlink-Schutz) -> Vorschaubild der Suche
		if fb := strings.TrimSpace(r.FormValue("fallback")); err != nil && fb != "" {
			if d2, e2, n2, err2 := downloadImage(ctx, fb); err2 == nil {
				data, ext, name, err = d2, e2, n2, nil
			}
		}
	} else {
		err = errors.New("Bitte ein Bild auswählen, eine URL angeben oder eine Datei hochladen")
	}
	if err != nil {
		partRedirect(w, r, id, back, "", err)
		return
	}
	if name == "" || name == "." || name == "/" || len(name) > 120 {
		name = "teilebild" + ext
	}
	rel := filepath.Join("inventory", id, "bild-"+randomHex(8)+ext)
	abs := filepath.Join("uploads", rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		partRedirect(w, r, id, back, "", err)
		return
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		partRedirect(w, r, id, back, "", err)
		return
	}
	mimetype := http.DetectContentType(data)
	if _, err := h.db.Exec(ctx, `
		INSERT INTO attachments (id, ref_type, ref_id, filename, filepath, mimetype, size_bytes, caption, created_by)
		VALUES (gen_random_uuid(), 'inventory', $1::uuid, $2, $3, $4, $5, 'Teilebild', $6)`,
		id, name, filepath.ToSlash(rel), mimetype, len(data), nullID(getUser(r).ID)); err != nil {
		_ = os.Remove(abs)
		partRedirect(w, r, id, back, "", err)
		return
	}
	partRedirect(w, r, id, back, "Teilebild übernommen", setImage("/uploads/"+filepath.ToSlash(rel)))
}

// downloadImage laedt ein Bild ueber den abgesicherten Client.
func downloadImage(ctx context.Context, raw string) ([]byte, string, string, error) {
	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := externalGet(fctx, raw, map[string]string{"Accept": "image/*"})
	if err != nil {
		return nil, "", "", fmt.Errorf("Bild konnte nicht geladen werden: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("Bild konnte nicht geladen werden (HTTP %d)", res.StatusCode)
	}
	data, ext, err := readImage(res.Body)
	return data, ext, filepath.Base(res.Request.URL.Path), err
}
