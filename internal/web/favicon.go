package web

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"pdh"
)

// AppIconFiles: oeffentliche App-Symbole unter der Wurzel (Browser fragen
// /favicon.ico auch ohne <link> ab). Quelle: web/static, eingebettet.
// sw.js ist der Service Worker fuer Push-Benachrichtigungen (push.go) – er muss
// unter der Wurzel liegen, damit er fuer alle Seiten gilt.
var AppIconFiles = []string{"favicon.ico", "favicon.svg", "favicon-32.png", "apple-touch-icon.png", "icon-192.png", "icon-512.png", "site.webmanifest", "sw.js"}

// AppIconHandler liefert die Datei aus web/static, deren Name dem Pfad entspricht.
func AppIconHandler() http.Handler {
	sub, err := fs.Sub(pdh.Static, "web/static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path.Ext(r.URL.Path) {
		case ".webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json")
		case ".js":
			// Service Worker: immer frisch pruefen, gilt fuer die ganze Seite
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Service-Worker-Allowed", "/")
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}

// staticAsset: Datei aus web/static, einmal gelesen und vorab gzip-komprimiert.
type staticAsset struct {
	raw, gz []byte
	etag    string
}

var staticAssets sync.Map // Pfad -> *staticAsset

func loadStaticAsset(name string) *staticAsset {
	if a, ok := staticAssets.Load(name); ok {
		return a.(*staticAsset)
	}
	raw, err := fs.ReadFile(pdh.Static, "web/static/"+name)
	if err != nil {
		return nil
	}
	a := newStaticAsset(raw)
	staticAssets.Store(name, a)
	return a
}

// plainAsset: kleine Datei ohne Komprimierung (nur ETag).
func plainAsset(raw []byte) *staticAsset {
	sum := sha256.Sum256(raw)
	return &staticAsset{raw: raw, etag: hex.EncodeToString(sum[:8])}
}

// newStaticAsset: Bytes mit ETag und vorab gzip-komprimierter Fassung.
func newStaticAsset(raw []byte) *staticAsset {
	a := plainAsset(raw)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(raw)
	zw.Close()
	a.gz = buf.Bytes()
	return a
}

// serveAsset liefert a komprimiert (falls vorhanden und der Browser gzip kann)
// mit ETag und dem gegebenen Cache-Control aus; name bestimmt den Dateityp.
func serveAsset(w http.ResponseWriter, r *http.Request, name string, a *staticAsset, cacheControl string) {
	h := w.Header()
	h.Add("Vary", "Accept-Encoding")
	h.Set("Cache-Control", cacheControl)
	body, etag := a.raw, `"`+a.etag+`"`
	if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		body, etag = a.gz, `"`+a.etag+`-gz"`
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("ETag", etag)
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(body))
}
