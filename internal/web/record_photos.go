package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"pdh/internal/modules/attachments"
)

// Fotos beim Anlegen eines Vorgangs (Erstellungs-Assistent, Leitstand,
// Easy-Mode). Gespeichert wie jeder Anhang (attachments, „Dokumente“ am
// Vorgang). Nur Bilder, höchstens recordPhotoMax je Meldung.
// Der Leitstand und der Easy-Mode arbeiten ohne Anmeldung: Der Leitstand
// bekommt beim Anlegen einen kurzlebigen, signierten Schlüssel nur für
// diesen Vorgang (photoToken); der Easy-Mode schickt die Fotos gleich mit.

const (
	recordPhotoMax     = 5
	recordPhotoMaxSize = 15 << 20
	photoTokenTTL      = 10 * time.Minute
)

// recordPhotoKinds: Vorgangsart → Tabelle (für created_by).
var recordPhotoKinds = map[string]string{"fault": "faults", "ticket": "tickets", "task": "tasks", "maintenance_task": "maintenance_tasks"}

func (h *Handler) photoTokenSig(kind, id string, exp int64) string {
	m := hmac.New(sha256.New, []byte("pdh-photo:"+h.jwtSecret))
	fmt.Fprintf(m, "%s:%s:%d", kind, id, exp)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// photoToken: Schlüssel zum Nachreichen von Fotos für genau diesen Vorgang.
func (h *Handler) photoToken(kind, id string, now time.Time) string {
	exp := now.Add(photoTokenTTL).Unix()
	return strconv.FormatInt(exp, 10) + "." + h.photoTokenSig(kind, id, exp)
}

func (h *Handler) checkPhotoToken(kind, id, tok string, now time.Time) bool {
	expS, sig, ok := strings.Cut(tok, ".")
	if !ok || h.jwtSecret == "" {
		return false
	}
	exp, err := strconv.ParseInt(expS, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(h.photoTokenSig(kind, id, exp)))
}

// saveRecordPhotos hängt die Bilder aus dem Feld „files“ (bereits geparstes
// Multipart-Formular) an den Vorgang. Andere Dateitypen und alles über
// recordPhotoMax werden verworfen. Liefert die Zahl gespeicherter Fotos.
func (h *Handler) saveRecordPhotos(ctx context.Context, kind, id, uid string, r *http.Request) (int, error) {
	if r.MultipartForm == nil {
		return 0, nil
	}
	var keep = r.MultipartForm.File["files"][:0:0]
	for _, fh := range r.MultipartForm.File["files"] {
		ct := strings.ToLower(fh.Header.Get("Content-Type"))
		name := strings.ToLower(fh.Filename)
		isImg := strings.HasPrefix(ct, "image/") || strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".webp")
		if !isImg || fh.Size > recordPhotoMaxSize || len(keep) >= recordPhotoMax {
			continue
		}
		keep = append(keep, fh)
	}
	r.MultipartForm.File["files"] = keep
	delete(r.MultipartForm.File, "file")
	if len(keep) == 0 {
		return 0, nil
	}
	if r.MultipartForm.Value == nil {
		r.MultipartForm.Value = map[string][]string{}
	}
	if len(r.MultipartForm.Value["caption"]) == 0 {
		r.MultipartForm.Value["caption"] = []string{"Foto aus der Meldung"}
	}
	if r.Form != nil && r.Form.Get("caption") == "" {
		r.Form.Set("caption", "Foto aus der Meldung")
	}
	saved, err := attachments.NewService(attachments.NewRepository(h.db)).Upload(ctx, kind, id, uid, r)
	return len(saved), err
}

// GlobalPhotosWeb: POST /global/photos/{kind}/{id}?token=… – Fotos zu einem
// gerade im Leitstand angelegten Vorgang nachreichen (ohne Anmeldung).
func (h *Handler) GlobalPhotosWeb(w http.ResponseWriter, r *http.Request) {
	kind, id := chi.URLParam(r, "kind"), chi.URLParam(r, "id")
	table, ok := recordPhotoKinds[kind]
	if !ok || !uuidInPathRe.MatchString(id) || !h.checkPhotoToken(kind, id, r.URL.Query().Get("token"), time.Now()) {
		writeGlobalBoardError(w, http.StatusForbidden, "Fotos können nur direkt nach dem Anlegen hinzugefügt werden")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, recordPhotoMax*recordPhotoMaxSize+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeGlobalBoardError(w, http.StatusRequestEntityTooLarge, "Fotos zu groß")
		return
	}
	ctx := r.Context()
	var creator string
	_ = h.db.QueryRow(ctx, `SELECT COALESCE(created_by::text, '') FROM `+table+` WHERE id = $1::uuid`, id).Scan(&creator)
	n, err := h.saveRecordPhotos(ctx, kind, id, firstNonEmpty(creator, pdhSystemUserID), r)
	if err != nil {
		componentLog("leitstand").Warn().Err(err).Str("art", kind).Str("id", id).Msg("fotos")
		writeGlobalBoardError(w, http.StatusInternalServerError, "Fotos konnten nicht gespeichert werden")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "photos": n})
}
