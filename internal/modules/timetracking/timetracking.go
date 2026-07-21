package timetracking

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Modelle ──────────────────────────────────────────────────

type RefType string

const (
	RefTicket      RefType = "ticket"
	RefFault       RefType = "fault"
	RefProject     RefType = "project"
	RefMaintenance RefType = "maintenance"
	RefProduction  RefType = "production"
)

type TimeEntry struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	RefType     RefType    `json:"ref_type"`
	RefID       string     `json:"ref_id"`
	Description string     `json:"description"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	DurationMin *int       `json:"duration_min,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`

	// Kostenstelle (Infrastruktur-Knoten), optional
	InfrastructureID *string `json:"infrastructure_id,omitempty"`

	// Joined
	UserName  string `json:"user_name,omitempty"`
	InfraName string `json:"infra_name,omitempty"`
}

type CreateEntryInput struct {
	RefType          RefType    `json:"ref_type"`
	RefID            string     `json:"ref_id"`
	Description      string     `json:"description"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	InfrastructureID *string    `json:"infrastructure_id,omitempty"`
}

type StopEntryInput struct {
	EndedAt time.Time `json:"ended_at"`
}

type Summary struct {
	RefType    RefType `json:"ref_type"`
	RefID      string  `json:"ref_id"`
	TotalMin   int     `json:"total_min"`
	TotalHours float64 `json:"total_hours"`
	EntryCount int     `json:"entry_count"`
}

// DayTotal: Summe der erfassten Minuten an einem einzelnen Tag (für Balkendiagramm)
type DayTotal struct {
	Date     string `json:"date"`
	TotalMin int    `json:"total_min"`
}

// CategoryTotal: Summe der erfassten Minuten je Zuordnung (Kostenstelle oder Typ, für Tortendiagramm)
type CategoryTotal struct {
	Label    string `json:"label"`
	TotalMin int    `json:"total_min"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, e *TimeEntry) error {
	query := `
		INSERT INTO time_entries (id, user_id, ref_type, ref_id, description, started_at, ended_at, duration_min, infrastructure_id)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`
	dur := calcDuration(e.StartedAt, e.EndedAt)
	return r.db.QueryRow(ctx, query,
		e.UserID, e.RefType, e.RefID, e.Description,
		e.StartedAt, e.EndedAt, dur, e.InfrastructureID,
	).Scan(&e.ID, &e.CreatedAt)
}

func (r *Repository) Stop(ctx context.Context, id, userID string, endedAt time.Time) error {
	_, err := r.db.Exec(ctx, `
		UPDATE time_entries
		SET ended_at=$1,
		    duration_min=EXTRACT(EPOCH FROM ($1 - started_at))/60
		WHERE id=$2 AND user_id=$3 AND ended_at IS NULL`,
		endedAt, id, userID)
	return err
}

func (r *Repository) GetByID(ctx context.Context, id string) (*TimeEntry, error) {
	e := &TimeEntry{}
	err := r.db.QueryRow(ctx, `
		SELECT te.id, te.user_id, te.ref_type, te.ref_id, te.description,
		       te.started_at, te.ended_at, te.duration_min, te.created_at,
		       te.infrastructure_id, COALESCE(i.name, ''),
		       u.first_name || ' ' || u.last_name
		FROM time_entries te
		JOIN users u ON te.user_id = u.id
		LEFT JOIN infrastructure i ON te.infrastructure_id = i.id
		WHERE te.id = $1`, id).Scan(
		&e.ID, &e.UserID, &e.RefType, &e.RefID, &e.Description,
		&e.StartedAt, &e.EndedAt, &e.DurationMin, &e.CreatedAt,
		&e.InfrastructureID, &e.InfraName, &e.UserName,
	)
	return e, err
}

func (r *Repository) ListByUser(ctx context.Context, userID, from, to string) ([]*TimeEntry, error) {
	rows, err := r.db.Query(ctx, `
		SELECT te.id, te.user_id, te.ref_type, te.ref_id, te.description,
		       te.started_at, te.ended_at, te.duration_min, te.created_at,
		       te.infrastructure_id, COALESCE(i.name, '')
		FROM time_entries te
		LEFT JOIN infrastructure i ON te.infrastructure_id = i.id
		WHERE te.user_id=$1 AND te.started_at::date BETWEEN $2 AND $3
		ORDER BY te.started_at DESC`,
		userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*TimeEntry
	for rows.Next() {
		e := &TimeEntry{}
		rows.Scan(&e.ID, &e.UserID, &e.RefType, &e.RefID, &e.Description,
			&e.StartedAt, &e.EndedAt, &e.DurationMin, &e.CreatedAt,
			&e.InfrastructureID, &e.InfraName)
		entries = append(entries, e)
	}
	return entries, nil
}

func (r *Repository) ListByRef(ctx context.Context, refType RefType, refID string) ([]*TimeEntry, error) {
	rows, err := r.db.Query(ctx, `
		SELECT te.id, te.user_id, te.ref_type, te.ref_id, te.description,
		       te.started_at, te.ended_at, te.duration_min, te.created_at,
		       te.infrastructure_id, COALESCE(i.name, ''),
		       u.first_name || ' ' || u.last_name
		FROM time_entries te
		JOIN users u ON te.user_id = u.id
		LEFT JOIN infrastructure i ON te.infrastructure_id = i.id
		WHERE te.ref_type=$1 AND te.ref_id=$2
		ORDER BY te.started_at DESC`,
		refType, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []*TimeEntry
	for rows.Next() {
		e := &TimeEntry{}
		rows.Scan(&e.ID, &e.UserID, &e.RefType, &e.RefID, &e.Description,
			&e.StartedAt, &e.EndedAt, &e.DurationMin, &e.CreatedAt,
			&e.InfrastructureID, &e.InfraName, &e.UserName)
		entries = append(entries, e)
	}
	return entries, nil
}

func (r *Repository) GetRunning(ctx context.Context, userID string) (*TimeEntry, error) {
	e := &TimeEntry{}
	err := r.db.QueryRow(ctx, `
		SELECT id, user_id, ref_type, ref_id, description, started_at, created_at
		FROM time_entries
		WHERE user_id=$1 AND ended_at IS NULL
		ORDER BY started_at DESC LIMIT 1`, userID).Scan(
		&e.ID, &e.UserID, &e.RefType, &e.RefID, &e.Description,
		&e.StartedAt, &e.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return e, nil
}

func (r *Repository) Summary(ctx context.Context, userID, from, to string) ([]*Summary, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ref_type, ref_id,
		       COALESCE(SUM(duration_min), 0) AS total_min,
		       COUNT(*) AS entry_count
		FROM time_entries
		WHERE user_id=$1
		  AND started_at::date BETWEEN $2 AND $3
		  AND ended_at IS NOT NULL
		GROUP BY ref_type, ref_id
		ORDER BY total_min DESC`,
		userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []*Summary
	for rows.Next() {
		s := &Summary{}
		rows.Scan(&s.RefType, &s.RefID, &s.TotalMin, &s.EntryCount)
		s.TotalHours = float64(s.TotalMin) / 60.0
		summaries = append(summaries, s)
	}
	return summaries, nil
}

// MonthlyByDay: Summe der Minuten je Tag im angegebenen Zeitraum (für Balkendiagramm)
func (r *Repository) MonthlyByDay(ctx context.Context, userID, from, to string) ([]*DayTotal, error) {
	rows, err := r.db.Query(ctx, `
		SELECT started_at::date::text AS day, COALESCE(SUM(duration_min), 0) AS total_min
		FROM time_entries
		WHERE user_id=$1 AND started_at::date BETWEEN $2 AND $3 AND ended_at IS NOT NULL
		GROUP BY started_at::date
		ORDER BY started_at::date`,
		userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var days []*DayTotal
	for rows.Next() {
		d := &DayTotal{}
		if err := rows.Scan(&d.Date, &d.TotalMin); err != nil {
			return nil, err
		}
		days = append(days, d)
	}
	return days, nil
}

// MonthlyByCategory: Summe der Minuten je Zuordnung (Kostenstelle falls gesetzt,
// sonst Typ-Label) im angegebenen Zeitraum (für Tortendiagramm)
func (r *Repository) MonthlyByCategory(ctx context.Context, userID, from, to string) ([]*CategoryTotal, error) {
	rows, err := r.db.Query(ctx, `
		SELECT COALESCE(NULLIF(i.name, ''), CASE te.ref_type
				WHEN 'ticket' THEN 'Ticket'
				WHEN 'fault' THEN 'Störung'
				WHEN 'maintenance' THEN 'Wartung'
				WHEN 'production' THEN 'Sonstiges'
				ELSE te.ref_type::text
			END) AS label,
			COALESCE(SUM(te.duration_min), 0) AS total_min
		FROM time_entries te
		LEFT JOIN infrastructure i ON te.infrastructure_id = i.id
		WHERE te.user_id=$1 AND te.started_at::date BETWEEN $2 AND $3 AND te.ended_at IS NOT NULL
		GROUP BY label
		ORDER BY total_min DESC`,
		userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cats []*CategoryTotal
	for rows.Next() {
		c := &CategoryTotal{}
		if err := rows.Scan(&c.Label, &c.TotalMin); err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, nil
}

func (r *Repository) Delete(ctx context.Context, id, userID string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM time_entries WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}

// ── Service ──────────────────────────────────────────────────

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Start(ctx context.Context, in *CreateEntryInput, userID string) (*TimeEntry, error) {
	e := &TimeEntry{
		UserID:           userID,
		RefType:          in.RefType,
		RefID:            in.RefID,
		Description:      in.Description,
		StartedAt:        in.StartedAt,
		EndedAt:          in.EndedAt,
		InfrastructureID: in.InfrastructureID,
	}
	if e.StartedAt.IsZero() {
		e.StartedAt = time.Now()
	}
	return e, s.repo.Create(ctx, e)
}

func (s *Service) Stop(ctx context.Context, id, userID string, endedAt time.Time) error {
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	return s.repo.Stop(ctx, id, userID, endedAt)
}

func (s *Service) GetRunning(ctx context.Context, userID string) (*TimeEntry, error) {
	return s.repo.GetRunning(ctx, userID)
}

func (s *Service) ListByUser(ctx context.Context, userID, from, to string) ([]*TimeEntry, error) {
	if from == "" {
		from = time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	return s.repo.ListByUser(ctx, userID, from, to)
}

func (s *Service) ListByRef(ctx context.Context, refType RefType, refID string) ([]*TimeEntry, error) {
	return s.repo.ListByRef(ctx, refType, refID)
}

func (s *Service) Summary(ctx context.Context, userID, from, to string) ([]*Summary, error) {
	if from == "" {
		from = time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	}
	if to == "" {
		to = time.Now().Format("2006-01-02")
	}
	return s.repo.Summary(ctx, userID, from, to)
}

// monthRange liefert den ersten und letzten Tag eines Kalendermonats als "2006-01-02"-Strings.
func monthRange(year, month int) (string, string) {
	first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	last := first.AddDate(0, 1, -1)
	return first.Format("2006-01-02"), last.Format("2006-01-02")
}

func (s *Service) MonthlyByDay(ctx context.Context, userID string, year, month int) ([]*DayTotal, error) {
	from, to := monthRange(year, month)
	return s.repo.MonthlyByDay(ctx, userID, from, to)
}

func (s *Service) MonthlyByCategory(ctx context.Context, userID string, year, month int) ([]*CategoryTotal, error) {
	from, to := monthRange(year, month)
	return s.repo.MonthlyByCategory(ctx, userID, from, to)
}

func (s *Service) ExportMonth(ctx context.Context, userID string, year, month int) ([]*TimeEntry, error) {
	from, to := monthRange(year, month)
	return s.repo.ListByUser(ctx, userID, from, to)
}

func (s *Service) Delete(ctx context.Context, id, userID string) error {
	return s.repo.Delete(ctx, id, userID)
}

// ── Handler ──────────────────────────────────────────────────

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Routes(jwtSecret string) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Auth(jwtSecret))

	r.Post("/", h.Start)
	r.Get("/", h.List)
	r.Get("/running", h.GetRunning)
	r.Get("/summary", h.Summary)
	r.Get("/monthly/days", h.MonthlyByDay)
	r.Get("/monthly/categories", h.MonthlyByCategory)
	r.Get("/export.csv", h.ExportCSV)
	r.Post("/{id}/stop", h.Stop)
	r.Delete("/{id}", h.Delete)
	r.Get("/ref/{type}/{id}", h.ListByRef)

	return r
}

func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var in CreateEntryInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	e, err := h.svc.Start(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, e)
}

func (h *Handler) Stop(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in StopEntryInput
	json.NewDecoder(r.Body).Decode(&in)
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.svc.Stop(r.Context(), id, userID, in.EndedAt); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gestoppt"})
}

func (h *Handler) GetRunning(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	e, err := h.svc.GetRunning(r.Context(), userID)
	if err != nil {
		response.JSON(w, 200, nil)
		return
	}
	response.JSON(w, 200, e)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	entries, err := h.svc.ListByUser(r.Context(), userID, from, to)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, entries)
}

func (h *Handler) ListByRef(w http.ResponseWriter, r *http.Request) {
	refType := RefType(chi.URLParam(r, "type"))
	refID := chi.URLParam(r, "id")
	entries, err := h.svc.ListByRef(r.Context(), refType, refID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, entries)
}

func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	summaries, err := h.svc.Summary(r.Context(), userID, from, to)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, summaries)
}

// parseYearMonth liest ?year=&month= aus der Query, mit dem aktuellen Monat als Vorgabe.
func parseYearMonth(r *http.Request) (int, int) {
	now := time.Now()
	year := now.Year()
	month := int(now.Month())
	if v := r.URL.Query().Get("year"); v != "" {
		if y, err := strconv.Atoi(v); err == nil {
			year = y
		}
	}
	if v := r.URL.Query().Get("month"); v != "" {
		if m, err := strconv.Atoi(v); err == nil && m >= 1 && m <= 12 {
			month = m
		}
	}
	return year, month
}

func (h *Handler) MonthlyByDay(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	year, month := parseYearMonth(r)
	days, err := h.svc.MonthlyByDay(r.Context(), userID, year, month)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, days)
}

func (h *Handler) MonthlyByCategory(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	year, month := parseYearMonth(r)
	cats, err := h.svc.MonthlyByCategory(r.Context(), userID, year, month)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, cats)
}

func (h *Handler) ExportCSV(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	year, month := parseYearMonth(r)
	entries, err := h.svc.ExportMonth(r.Context(), userID, year, month)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}

	filename := fmt.Sprintf("zeiterfassung_%04d-%02d.csv", year, month)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)

	cw := csv.NewWriter(w)
	cw.Write([]string{"Datum", "Start", "Ende", "Dauer (Minuten)", "Typ", "Kostenstelle", "Beschreibung"})
	for _, e := range entries {
		endedStr := ""
		if e.EndedAt != nil {
			endedStr = e.EndedAt.Format("15:04")
		}
		durStr := ""
		if e.DurationMin != nil {
			durStr = strconv.Itoa(*e.DurationMin)
		}
		cw.Write([]string{
			e.StartedAt.Format("2006-01-02"),
			e.StartedAt.Format("15:04"),
			endedStr,
			durStr,
			string(e.RefType),
			e.InfraName,
			e.Description,
		})
	}
	cw.Flush()
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	if err := h.svc.Delete(r.Context(), id, userID); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gelöscht"})
}

// ── Helpers ──────────────────────────────────────────────────

func calcDuration(start time.Time, end *time.Time) *int {
	if end == nil {
		return nil
	}
	min := int(end.Sub(start).Minutes())
	return &min
}

func formatDuration(min int) string {
	h := min / 60
	m := min % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dmin", h, m)
	}
	return fmt.Sprintf("%dmin", m)
}
