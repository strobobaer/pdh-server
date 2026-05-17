package attachments

import (
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
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

const UploadDir = "uploads"

type Attachment struct {
	ID        string    `json:"id"`
	RefType   string    `json:"ref_type"`
	RefID     string    `json:"ref_id"`
	Filename  string    `json:"filename"`
	Filepath  string    `json:"filepath"`
	Mimetype  string    `json:"mimetype"`
	SizeBytes int       `json:"size_bytes"`
	Caption   string    `json:"caption,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	URL       string    `json:"url"`
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
	rows, err := r.db.Query(ctx, `
		SELECT id, ref_type, ref_id, filename, filepath, mimetype,
		       size_bytes, COALESCE(caption,''), created_by, created_at
		FROM attachments WHERE ref_type=$1 AND ref_id=$2
		ORDER BY created_at DESC`, refType, refID)
	if err != nil { return nil, err }
	defer rows.Close()

	var list []*Attachment
	for rows.Next() {
		a := &Attachment{}
		rows.Scan(&a.ID, &a.RefType, &a.RefID, &a.Filename,
			&a.Filepath, &a.Mimetype, &a.SizeBytes,
			&a.Caption, &a.CreatedBy, &a.CreatedAt)
		a.URL = "/uploads/" + a.Filepath
		list = append(list, a)
	}
	return list, nil
}

func (r *Repository) Delete(ctx context.Context, id, userID string) (string, error) {
	var fp string
	r.db.QueryRow(ctx, `DELETE FROM attachments WHERE id=$1 AND created_by=$2 RETURNING filepath`, id, userID).Scan(&fp)
	return fp, nil
}

// ── Service ──────────────────────────────────────────────────

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
	if len(files) == 0 { files = r.MultipartForm.File["file"] }

	for _, fh := range files {
		if fh.Size > 20<<20 { continue } // max 20MB

		src, err := fh.Open()
		if err != nil { continue }
		defer src.Close()

		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext == "" { ext = ".jpg" }
	allowed := map[string]bool{
		".jpg":true,".jpeg":true,".png":true,".gif":true,".webp":true,
		".pdf":true,".doc":true,".docx":true,
		".xls":true,".xlsx":true,".txt":true,".csv":true,
	}
	if !allowed[ext] { continue }
		fname := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
		relPath := filepath.Join(refType, refID, fname)
		absPath := filepath.Join(UploadDir, relPath)

		dst, err := os.Create(absPath)
		if err != nil { continue }
		written, _ := io.Copy(dst, src)
		dst.Close()

		mime := fh.Header.Get("Content-Type")
		if mime == "" { mime = "image/jpeg" }

		caption := r.FormValue("caption")
		a := &Attachment{
			RefType: refType, RefID: refID,
			Filename: fh.Filename, Filepath: relPath,
			Mimetype: mime, SizeBytes: int(written),
			Caption: caption, CreatedBy: userID,
		}
		if err := s.repo.Save(ctx, a); err == nil {
			a.URL = "/uploads/" + relPath
			saved = append(saved, a)
		}
	}
	return saved, nil
}

func (s *Service) List(ctx context.Context, refType, refID string) ([]*Attachment, error) {
	return s.repo.List(ctx, refType, refID)
}

func (s *Service) Delete(ctx context.Context, id, userID string) error {
	fp, _ := s.repo.Delete(ctx, id, userID)
	if fp != "" { os.Remove(filepath.Join(UploadDir, fp)) }
	return nil
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct{ svc *Service }
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))
	r.Post("/{refType}/{refID}", h.Upload)
	r.Get("/{refType}/{refID}", h.List)
	r.Delete("/{id}", h.Delete)
	return r
}

func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	refType := chi.URLParam(r, "refType")
	refID   := chi.URLParam(r, "refID")
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	files, err := h.svc.Upload(r.Context(), refType, refID, userID, r)
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 201, files)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	files, err := h.svc.List(r.Context(), chi.URLParam(r, "refType"), chi.URLParam(r, "refID"))
	if err != nil { response.Error(w, 500, err.Error()); return }
	response.JSON(w, 200, files)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	h.svc.Delete(r.Context(), chi.URLParam(r, "id"), userID)
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}
