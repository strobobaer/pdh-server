package web

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/gif"
	_ "image/png"

	"github.com/go-chi/chi/v5"
)

// Profilbilder: hochladen auf „Mein Konto“ bzw. im Benutzerstamm (Admins),
// serverseitig quadratisch zugeschnitten und auf 256 px als JPEG gespeichert
// (uploads/avatars, mit der Sicherung „Anhänge & Bilder“). Das Neu-Kodieren
// entfernt auch Metadaten wie den Aufnahmeort. Ausgeliefert über /avatar/{id}
// (nur angemeldet); der Systembenutzer „Service“ zeigt das Logo.

const (
	avatarDir     = "uploads/avatars"
	avatarSize    = 256
	avatarMaxSize = 8 << 20
)

// avatarURL: Adresse des Profilbilds ("" = keins, dann Initialen).
func avatarURL(uid, path string) string {
	if uid == pdhSystemUserID {
		return "/avatar/service"
	}
	if path == "" {
		return ""
	}
	sum := sha1.Sum([]byte(path)) // neue Datei = neue Adresse (Browser-Cache)
	return "/avatar/" + uid + "?v=" + hex.EncodeToString(sum[:4])
}

// avatarPaths: Profilbild aller Benutzer (für Listen ohne Einzelabfragen).
func (h *Handler) avatarPaths(ctx context.Context) map[string]string {
	out := map[string]string{}
	if h.db == nil {
		return out
	}
	rows, err := h.db.Query(ctx, `SELECT id::text, avatar_path FROM users WHERE avatar_path IS NOT NULL AND avatar_path <> ''`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, p string
		if rows.Scan(&id, &p) == nil {
			out[id] = p
		}
	}
	return out
}

func (h *Handler) userAvatarURL(ctx context.Context, uid string) string {
	if uid == "" || h.db == nil {
		return ""
	}
	var p *string
	if row := userRowFrom(ctx, uid); row != nil {
		p = row.avatarPath
	} else {
		_ = h.db.QueryRow(ctx, `SELECT avatar_path FROM users WHERE id = $1::uuid`, uid).Scan(&p)
	}
	if p == nil {
		return avatarURL(uid, "")
	}
	return avatarURL(uid, *p)
}

// AvatarWeb: GET /avatar/{id} – Profilbild bzw. Logo für „Service“.
func (h *Handler) AvatarWeb(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "service" || id == pdhSystemUserID {
		logo := strings.TrimSpace(h.branding().AppLogo)
		if logo == "" {
			logo = "/favicon.svg"
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		http.Redirect(w, r, logo, http.StatusFound)
		return
	}
	if !uuidInPathRe.MatchString(id) || h.db == nil {
		http.NotFound(w, r)
		return
	}
	var p *string
	if h.db.QueryRow(r.Context(), `SELECT avatar_path FROM users WHERE id = $1::uuid`, id).Scan(&p) != nil || p == nil || *p == "" {
		http.NotFound(w, r)
		return
	}
	file := filepath.Join(avatarDir, filepath.Base(*p)) // nur Dateien aus dem Profilbild-Ordner
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, file)
}

// avatarProcess: Bild lesen, mittig quadratisch zuschneiden, auf 256 px skalieren, als JPEG.
func avatarProcess(rd io.Reader) ([]byte, error) {
	img, _, err := image.Decode(io.LimitReader(rd, avatarMaxSize))
	if err != nil {
		return nil, errors.New("Bild nicht lesbar – bitte JPG, PNG oder GIF verwenden")
	}
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	if side < 16 {
		return nil, errors.New("Bild zu klein")
	}
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	out := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	scale := float64(side) / avatarSize
	// Mittelwert über die Quellpixel je Zielpixel (sauber auch bei starker Verkleinerung)
	for y := 0; y < avatarSize; y++ {
		sy0, sy1 := y0+int(float64(y)*scale), y0+int(float64(y+1)*scale)
		sy1 = max(sy1, sy0+1)
		for x := 0; x < avatarSize; x++ {
			sx0, sx1 := x0+int(float64(x)*scale), x0+int(float64(x+1)*scale)
			sx1 = max(sx1, sx0+1)
			var r, g, bl, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					// transparente Bereiche auf Weiß
					cr, cg, cb = cr+(0xffff-ca), cg+(0xffff-ca), cb+(0xffff-ca)
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			out.Set(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// canEditAvatar: eigenes Bild immer, fremdes mit „Benutzer verwalten“.
func (h *Handler) canEditAvatar(r *http.Request, uid string) bool {
	u := getUser(r)
	return u.ID != "" && (u.ID == uid || h.rbac.HasPermissionForUser(u.ID, string(u.Role), "system.manage_users"))
}

func avatarBack(r *http.Request, uid string) string {
	if b := r.FormValue("back"); strings.HasPrefix(b, "/") && !strings.HasPrefix(b, "//") {
		return b
	}
	if uid == getUser(r).ID {
		return "/account"
	}
	return "/users/" + uid + "?tab=master"
}

// withNotice hängt die Rückmeldung an: Benutzerstamm zeigt msg/err, „Mein Konto“ notice.
func withNotice(back, key, msg string) string {
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	if !strings.HasPrefix(back, "/users/") {
		if key == "err" {
			msg = "Fehler: " + msg
		}
		key = "notice"
	} else if key == "notice" {
		key = "msg"
	}
	return back + sep + key + "=" + url.QueryEscape(msg)
}

// AvatarUploadWeb: POST /users/{id}/avatar (multipart „avatar“) bzw. mit remove=1 löschen.
func (h *Handler) AvatarUploadWeb(w http.ResponseWriter, r *http.Request) {
	uid := chi.URLParam(r, "id")
	if uid == "me" {
		uid = getUser(r).ID
	}
	if !uuidInPathRe.MatchString(uid) || uid == pdhSystemUserID || !h.canEditAvatar(r, uid) {
		http.Error(w, "keine berechtigung", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, avatarMaxSize+(1<<20))
	if err := r.ParseMultipartForm(avatarMaxSize); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Redirect(w, r, withNotice(avatarBack(r, uid), "err", "Bild zu groß (höchstens 8 MB)"), http.StatusSeeOther)
		return
	}
	back := avatarBack(r, uid)
	ctx := r.Context()
	var old *string
	_ = h.db.QueryRow(ctx, `SELECT avatar_path FROM users WHERE id = $1::uuid`, uid).Scan(&old)
	removeOld := func() {
		if old != nil && *old != "" {
			_ = os.Remove(filepath.Join(avatarDir, filepath.Base(*old)))
		}
	}
	if r.FormValue("remove") == "1" {
		if _, err := h.db.Exec(ctx, `UPDATE users SET avatar_path = NULL WHERE id = $1::uuid`, uid); err != nil {
			http.Error(w, "Profilbild konnte nicht entfernt werden", http.StatusInternalServerError)
			return
		}
		removeOld()
		h.addHistory(ctx, "user", uid, "update", "Profilbild", "", "", "Profilbild entfernt", getUser(r).ID)
		http.Redirect(w, r, withNotice(back, "notice", "Profilbild entfernt"), http.StatusSeeOther)
		return
	}
	f, _, err := r.FormFile("avatar")
	if err != nil {
		http.Redirect(w, r, withNotice(back, "err", "Bitte ein Bild auswählen"), http.StatusSeeOther)
		return
	}
	defer f.Close()
	data, err := avatarProcess(f)
	if err != nil {
		http.Redirect(w, r, withNotice(back, "err", err.Error()), http.StatusSeeOther)
		return
	}
	if err := os.MkdirAll(avatarDir, 0o755); err != nil {
		http.Error(w, "Ordner für Profilbilder nicht anlegbar", http.StatusInternalServerError)
		return
	}
	name := uid + "-" + time.Now().Format("20060102150405") + ".jpg"
	if err := os.WriteFile(filepath.Join(avatarDir, name), data, 0o644); err != nil {
		http.Error(w, "Profilbild konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	if _, err := h.db.Exec(ctx, `UPDATE users SET avatar_path = $2 WHERE id = $1::uuid`, uid, name); err != nil {
		_ = os.Remove(filepath.Join(avatarDir, name))
		http.Error(w, "Profilbild konnte nicht gespeichert werden", http.StatusInternalServerError)
		return
	}
	removeOld()
	h.addHistory(ctx, "user", uid, "update", "Profilbild", "", "", "Profilbild geändert", getUser(r).ID)
	http.Redirect(w, r, withNotice(back, "notice", "Profilbild gespeichert"), http.StatusSeeOther)
}
