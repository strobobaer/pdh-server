package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uploads/esppdh bleibt gesperrt – auch über einen zweiten Weg zum selben Ordner (Verweis im
// Dateisystem, z. B. uploads/uploads -> uploads, wie es ihn auf echten Installationen gab) und
// mit Namen, die Windows wie „esppdh“ öffnet („esppdh.“, „esppdh “, „esppdh::$DATA“).

// uploadsTestDir: uploads mit Firmware unter esppdh/dev und einer anderen Datei unter avatars.
func uploadsTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "esppdh", "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "esppdh", "dev", "x.bin.enc"), []byte("GEHEIM"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "avatars"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "avatars", "a.txt"), []byte("BILD"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func uploadsGet(h http.Handler, p string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.URL.Path, r.URL.RawPath = p, ""
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func uploadsAssertHidden(t *testing.T, h http.Handler, p string) {
	t.Helper()
	if w := uploadsGet(h, p); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "GEHEIM") || strings.Contains(w.Body.String(), "x.bin.enc") || strings.Contains(w.Body.String(), "dev/") {
		t.Errorf("%s: %d %q", p, w.Code, w.Body.String())
	}
}

// Windows übergeht Punkte und Leerzeichen am Ende eines Namens und öffnet mit „:…“ einen
// Datenstrom desselben Ordners – solche Namen gelten wie „esppdh“.
func TestUploadsHiddenWindowsNames(t *testing.T) {
	for _, p := range []string{"/uploads/esppdh./dev/x.bin.enc", "/uploads/esppdh /dev/x.bin.enc", "/uploads/esppdh. . ./", "/uploads/ESPPDH../",
		"/uploads/esppdh::$INDEX_ALLOCATION/dev/x.bin.enc", "/uploads/esppdh:x/", "/uploads/.../esppdh/dev/x.bin.enc", "/uploads/ ./esppdh/"} {
		if !uploadsHidden(p) {
			t.Errorf("%s: nicht gesperrt", p)
		}
	}
	for _, p := range []string{"/uploads/avatars/a.txt", "/uploads/esppdh-alt/x", "/uploads/x/esppdh/y", "/uploads/esppdhx/"} {
		if uploadsHidden(p) {
			t.Errorf("%s: gesperrt, sollte frei sein", p)
		}
	}
	h := UploadsHandler(uploadsTestDir(t))
	for _, p := range []string{"/uploads/esppdh./dev/x.bin.enc", "/uploads/esppdh /dev/x.bin.enc", "/uploads/esppdh../"} {
		uploadsAssertHidden(t, h, p)
	}
	if w := uploadsGet(h, "/uploads/avatars/a.txt"); w.Code != http.StatusOK || w.Body.String() != "BILD" {
		t.Errorf("andere Datei: %d %q", w.Code, w.Body.String())
	}
}

// Ein Verweis im Dateisystem führt zum selben Ordner: gesperrt bleibt, was wirklich in
// uploads/esppdh liegt. Unter Windows lassen sich Verweise meist nur mit Sonderrechten
// anlegen – dann wird der Test übersprungen (Linux prüft ihn immer).
func TestUploadsHiddenThroughAlias(t *testing.T) {
	dir := uploadsTestDir(t)
	if err := os.Symlink(dir, filepath.Join(dir, "uploads")); err != nil {
		t.Skipf("Verweis lässt sich hier nicht anlegen: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "esppdh"), filepath.Join(dir, "fw")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "esppdh", "dev"), filepath.Join(dir, "avatars", "dev")); err != nil {
		t.Fatal(err)
	}
	h := UploadsHandler(dir)
	for _, p := range []string{"/uploads/uploads/esppdh/dev/x.bin.enc", "/uploads/uploads/esppdh/", "/uploads/uploads/uploads/esppdh/dev/x.bin.enc",
		"/uploads/fw/dev/x.bin.enc", "/uploads/fw/", "/uploads/fw", "/uploads/FW/dev/x.bin.enc", "/uploads/avatars/dev/x.bin.enc", "/uploads/avatars/dev/"} {
		uploadsAssertHidden(t, h, p)
	}
	// über den Verweis bleibt alles andere erreichbar
	if w := uploadsGet(h, "/uploads/uploads/avatars/a.txt"); w.Code != http.StatusOK || w.Body.String() != "BILD" {
		t.Errorf("andere Datei über den Verweis: %d %q", w.Code, w.Body.String())
	}
	// auch wenn uploads selbst ein Verweis ist (z. B. auf eine andere Platte)
	link := filepath.Join(t.TempDir(), "uploads")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	h = UploadsHandler(link)
	for _, p := range []string{"/uploads/esppdh/dev/x.bin.enc", "/uploads/fw/dev/x.bin.enc", "/uploads/uploads/esppdh/dev/x.bin.enc"} {
		uploadsAssertHidden(t, h, p)
	}
	if w := uploadsGet(h, "/uploads/avatars/a.txt"); w.Code != http.StatusOK || w.Body.String() != "BILD" {
		t.Errorf("andere Datei: %d %q", w.Code, w.Body.String())
	}
}

// Keine Ordnerlisten unter /uploads/: Ordner (mit und ohne „/“) antworten 404, Dateien liefern
// weiter, und uploads/esppdh bleibt gesperrt.
func TestUploadsNoDirectoryListing(t *testing.T) {
	dir := uploadsTestDir(t)
	h := UploadsHandler(dir)
	for _, p := range []string{"/uploads/", "/uploads", "/uploads/avatars/", "/uploads/avatars", "/uploads/./avatars/", "/uploads/avatars/../avatars"} {
		if w := uploadsGet(h, p); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "a.txt") {
			t.Errorf("%s: %d %q", p, w.Code, w.Body.String())
		}
	}
	if w := uploadsGet(h, "/uploads/avatars/a.txt"); w.Code != http.StatusOK || w.Body.String() != "BILD" {
		t.Errorf("Datei: %d %q", w.Code, w.Body.String())
	}
	uploadsAssertHidden(t, h, "/uploads/esppdh/")
	uploadsAssertHidden(t, h, "/uploads/esppdh/dev/x.bin.enc")
}
