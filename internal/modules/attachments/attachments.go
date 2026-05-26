package attachments

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"pdh/internal/integrations/nextcloud"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

const UploadDir = "uploads"

type Attachment struct {
	ID            string    `json:"id"`
	RefType       string    `json:"ref_type"`
	RefID         string    `json:"ref_id"`
	Filename      string    `json:"filename"`
	Filepath      string    `json:"filepath"`
	Mimetype      string    `json:"mimetype"`
	SizeBytes     int       `json:"size_bytes"`
	Caption       string    `json:"caption,omitempty"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	URL           string    `json:"url"`
	NextcloudPath string    `json:"nextcloud_path,omitempty"`
	IsImage       bool      `json:"is_image"`
	RecordImage   bool      `json:"record_image"`
}

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) Save(ctx context.Context, a *Attachment) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO attachments (id, ref_type, ref_id, filename, filepath, mimetype, size_bytes, caption, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		a.RefType, a.RefID, a.Filename, a.Filepath,
		a.Mimetype, a.SizeBytes, a.Caption, a.CreatedBy,
	).Scan(&a.ID, &a.CreatedAt)
}

func (r *Repository) List(ctx context.Context, refType, refID string) ([]*Attachment, error) {
	recordImageID, _ := r.RecordImageID(ctx, refType, refID)
	rows, err := r.db.Query(ctx, `
		SELECT id, ref_type, ref_id, filename, filepath, mimetype,
		       size_bytes, COALESCE(caption,''), created_by, created_at
		FROM attachments WHERE ref_type=$1 AND ref_id=$2
		ORDER BY created_at DESC`, refType, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*Attachment
	for rows.Next() {
		a := &Attachment{}
		rows.Scan(&a.ID, &a.RefType, &a.RefID, &a.Filename,
			&a.Filepath, &a.Mimetype, &a.SizeBytes,
			&a.Caption, &a.CreatedBy, &a.CreatedAt)
		a.URL = "/uploads/" + a.Filepath
		a.NextcloudPath = nextcloudRemotePath(a.RefType, a.RefID, a.Filename)
		a.IsImage = strings.HasPrefix(strings.ToLower(a.Mimetype), "image/")
		a.RecordImage = recordImageID != "" && a.ID == recordImageID
		list = append(list, a)
	}
	return list, nil
}

func (r *Repository) RecordImageID(ctx context.Context, refType, refID string) (string, error) {
	var table string
	switch refType {
	case "ticket":
		table = "tickets"
	case "fault":
		table = "faults"
	case "maintenance_task":
		table = "maintenance_tasks"
	default:
		return "", nil
	}
	var id *string
	err := r.db.QueryRow(ctx, fmt.Sprintf(`SELECT record_image_attachment_id::text FROM %s WHERE id=$1`, table), refID).Scan(&id)
	if err != nil || id == nil {
		return "", err
	}
	return *id, nil
}

func (r *Repository) SetRecordImage(ctx context.Context, refType, refID, id string) error {
	var table string
	switch refType {
	case "ticket":
		table = "tickets"
	case "fault":
		table = "faults"
	case "maintenance_task":
		table = "maintenance_tasks"
	default:
		return fmt.Errorf("ref_type %q unterstützt kein datensatzbild", refType)
	}
	var ok bool
	if err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM attachments
			WHERE id=$1 AND ref_type=$2 AND ref_id=$3 AND lower(mimetype) LIKE 'image/%'
		)`, id, refType, refID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bild nicht gefunden")
	}
	_, err := r.db.Exec(ctx, fmt.Sprintf(`UPDATE %s SET record_image_attachment_id=$1 WHERE id=$2`, table), id, refID)
	return err
}

func (r *Repository) Delete(ctx context.Context, id, userID string) (string, error) {
	var fp string
	err := r.db.QueryRow(ctx, `DELETE FROM attachments WHERE id=$1 AND created_by=$2 RETURNING filepath`, id, userID).Scan(&fp)
	if err == nil {
		_, _ = r.db.Exec(ctx, `UPDATE tickets SET record_image_attachment_id=NULL WHERE record_image_attachment_id=$1`, id)
		_, _ = r.db.Exec(ctx, `UPDATE faults SET record_image_attachment_id=NULL WHERE record_image_attachment_id=$1`, id)
		_, _ = r.db.Exec(ctx, `UPDATE maintenance_tasks SET record_image_attachment_id=NULL WHERE record_image_attachment_id=$1`, id)
	}
	return fp, err
}

// Service

type Service struct{ repo *Repository }

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

func (s *Service) Upload(ctx context.Context, refType, refID, userID string, r *http.Request) ([]*Attachment, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, fmt.Errorf("formular parsen: %w", err)
	}

	subDir := filepath.Join(UploadDir, refType, refID)
	os.MkdirAll(subDir, 0755)

	var saved []*Attachment
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		files = r.MultipartForm.File["file"]
	}

	ncClient := nextcloudClientFromEnv()

	for _, fh := range files {
		if fh.Size > 20<<20 {
			log.Warn().Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Int64("size", fh.Size).Msg("attachment übersprungen: datei zu groß")
			continue
		}

		src, err := fh.Open()
		if err != nil {
			log.Warn().Err(err).Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Msg("attachment öffnen fehlgeschlagen")
			continue
		}
		defer src.Close()

		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext == "" {
			ext = ".jpg"
		}
		allowed := map[string]bool{
			".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
			".pdf": true, ".doc": true, ".docx": true,
			".xls": true, ".xlsx": true, ".txt": true, ".csv": true,
		}
		if !allowed[ext] {
			log.Warn().Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Str("ext", ext).Msg("attachment übersprungen: dateityp nicht erlaubt")
			continue
		}
		fname := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
		relPath := filepath.Join(refType, refID, fname)
		absPath := filepath.Join(UploadDir, relPath)

		dst, err := os.Create(absPath)
		if err != nil {
			log.Error().Err(err).Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Str("path", absPath).Msg("attachment lokal speichern fehlgeschlagen")
			continue
		}
		var buf bytes.Buffer
		written, copyErr := io.Copy(io.MultiWriter(dst, &buf), src)
		closeErr := dst.Close()
		if copyErr != nil {
			log.Error().Err(copyErr).Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Msg("attachment lokal kopieren fehlgeschlagen")
			continue
		}
		if closeErr != nil {
			log.Error().Err(closeErr).Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Msg("attachment datei schließen fehlgeschlagen")
			continue
		}

		mime := fh.Header.Get("Content-Type")
		if mime == "" {
			mime = "image/jpeg"
		}

		caption := r.FormValue("caption")
		a := &Attachment{
			RefType: refType, RefID: refID,
			Filename: fh.Filename, Filepath: relPath,
			Mimetype: mime, SizeBytes: int(written),
			Caption: caption, CreatedBy: userID,
		}
		if err := s.repo.Save(ctx, a); err == nil {
			a.URL = "/uploads/" + relPath
			a.IsImage = strings.HasPrefix(strings.ToLower(a.Mimetype), "image/")
			if ncClient.Enabled() {
				if p, err := ncClient.UploadAttachment(ctx, refType, refID, fh.Filename, bytes.NewReader(buf.Bytes())); err == nil {
					a.NextcloudPath = p
					log.Info().Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Str("nextcloud_path", p).Msg("attachment nach nextcloud gespiegelt")
				} else {
					log.Error().Err(err).Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Msg("attachment nextcloud webdav spiegelung fehlgeschlagen")
				}
			} else {
				log.Debug().Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Msg("nextcloud spiegelung deaktiviert")
			}
			_, _ = s.repo.db.Exec(ctx, `
				INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, created_by, message)
				VALUES ($1, $2, 'attachment', 'attachments', $3, $4, 'Anhang hinzugefügt')`,
				refType, refID, a.Filename, userID)
			saved = append(saved, a)
		} else {
			log.Error().Err(err).Str("ref_type", refType).Str("ref_id", refID).Str("filename", fh.Filename).Msg("attachment metadaten speichern fehlgeschlagen")
		}
	}
	return saved, nil
}

func (s *Service) List(ctx context.Context, refType, refID string) ([]*Attachment, error) {
	return s.repo.List(ctx, refType, refID)
}

func (s *Service) Delete(ctx context.Context, id, userID string) error {
	fp, _ := s.repo.Delete(ctx, id, userID)
	if fp != "" {
		os.Remove(filepath.Join(UploadDir, fp))
	}
	return nil
}

func (s *Service) SetRecordImage(ctx context.Context, refType, refID, id string) error {
	if err := s.repo.SetRecordImage(ctx, refType, refID, id); err != nil {
		return err
	}
	_, _ = s.repo.db.Exec(ctx, `
		INSERT INTO record_history (ref_type, ref_id, action, field_name, new_value, message)
		VALUES ($1, $2, 'record_image', 'record_image_attachment_id', $3, 'Datensatzbild gesetzt')`,
		refType, refID, id)
	return nil
}

// Handler

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Post("/{refType}/{refID}", h.Upload)
	r.Get("/{refType}/{refID}", h.List)
	r.Post("/{refType}/{refID}/{id}/record-image", h.SetRecordImage)
	r.Delete("/{id}", h.Delete)
	return r
}

func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	refType := chi.URLParam(r, "refType")
	refID := chi.URLParam(r, "refID")
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	files, err := h.svc.Upload(r.Context(), refType, refID, userID, r)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, files)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	files, err := h.svc.List(r.Context(), chi.URLParam(r, "refType"), chi.URLParam(r, "refID"))
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, files)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	h.svc.Delete(r.Context(), chi.URLParam(r, "id"), userID)
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

func (h *Handler) SetRecordImage(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.SetRecordImage(r.Context(), chi.URLParam(r, "refType"), chi.URLParam(r, "refID"), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 400, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "ok"})
}

func nextcloudClientFromEnv() *nextcloud.Client {
	return nextcloud.NewClient(nextcloud.Config{
		Enabled:  strings.EqualFold(os.Getenv("PDH_NEXTCLOUD_ENABLED"), "true") || os.Getenv("PDH_NEXTCLOUD_ENABLED") == "1",
		BaseURL:  os.Getenv("PDH_NEXTCLOUD_BASEURL"),
		Username: os.Getenv("PDH_NEXTCLOUD_USERNAME"),
		Password: os.Getenv("PDH_NEXTCLOUD_PASSWORD"),
		RootPath: os.Getenv("PDH_NEXTCLOUD_ROOTPATH"),
	})
}

func nextcloudRemotePath(refType, refID, filename string) string {
	root := os.Getenv("PDH_NEXTCLOUD_ROOTPATH")
	if root == "" {
		root = "PDH"
	}
	module := map[string]string{"ticket": "Tickets", "fault": "Stoerungen", "maintenance_task": "Wartung"}[refType]
	if module == "" {
		module = refType
	}
	return strings.Trim(root, "/") + "/" + module + "/" + refID + "/" + filename
}
