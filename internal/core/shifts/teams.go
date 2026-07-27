package shifts

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Typen ────────────────────────────────────────────────────

// ShiftTeam: eine erweiterbare Liste von Teams mit fester Telefonnummer
// (z.B. ein Diensthandy), analog zu den Kostenstellen.
type ShiftTeam struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Phone     string    `json:"phone"`
	Active    bool      `json:"active"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateTeamInput struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

// LocksmithTeamAssignment: welches Team liefert die Nummer fuer
// "Schichtschlosser 1" (slot=1) bzw. "Schichtschlosser 2" (slot=2). Diese
// Zuordnung ist fix und unabhaengig davon, welcher Mitarbeiter gerade als
// Schichtschlosser markiert ist.
type LocksmithTeamAssignment struct {
	Slot     int     `json:"slot"`
	TeamID   *string `json:"team_id,omitempty"`
	TeamName string  `json:"team_name,omitempty"`
	Phone    string  `json:"phone,omitempty"`
}

// ── Repository ───────────────────────────────────────────────

func (r *Repository) CreateTeam(ctx context.Context, t *ShiftTeam) error {
	return r.db.QueryRow(ctx, `
		INSERT INTO shift_teams (id, name, phone, created_by)
		VALUES (gen_random_uuid(), $1, $2, $3)
		RETURNING id, active, created_at`,
		t.Name, t.Phone, t.CreatedBy,
	).Scan(&t.ID, &t.Active, &t.CreatedAt)
}

func (r *Repository) ListTeams(ctx context.Context) ([]*ShiftTeam, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, name, phone, active, created_by, created_at
		FROM shift_teams WHERE active=true ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var teams []*ShiftTeam
	for rows.Next() {
		t := &ShiftTeam{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Phone, &t.Active, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (r *Repository) UpdateTeam(ctx context.Context, id, name, phone string) error {
	_, err := r.db.Exec(ctx, `UPDATE shift_teams SET name=COALESCE(NULLIF($1,''), name), phone=$2 WHERE id=$3`, name, phone, id)
	return err
}

func (r *Repository) DeactivateTeam(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, `UPDATE shift_teams SET active=false WHERE id=$1`, id)
	return err
}

func (r *Repository) SetLocksmithTeam(ctx context.Context, slot int, teamID *string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO shift_locksmith_team_assignment (slot, team_id) VALUES ($1, $2)
		ON CONFLICT (slot) DO UPDATE SET team_id=$2`, slot, teamID)
	return err
}

func (r *Repository) GetLocksmithAssignments(ctx context.Context) ([]*LocksmithTeamAssignment, error) {
	rows, err := r.db.Query(ctx, `
		SELECT a.slot, a.team_id, COALESCE(t.name,''), COALESCE(t.phone,'')
		FROM shift_locksmith_team_assignment a
		LEFT JOIN shift_teams t ON a.team_id = t.id
		ORDER BY a.slot`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LocksmithTeamAssignment
	for rows.Next() {
		a := &LocksmithTeamAssignment{}
		if err := rows.Scan(&a.Slot, &a.TeamID, &a.TeamName, &a.Phone); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── Service ──────────────────────────────────────────────────

func (s *Service) CreateTeam(ctx context.Context, in *CreateTeamInput, userID string) (*ShiftTeam, error) {
	t := &ShiftTeam{Name: in.Name, Phone: in.Phone, CreatedBy: userID}
	return t, s.repo.CreateTeam(ctx, t)
}
func (s *Service) ListTeams(ctx context.Context) ([]*ShiftTeam, error) { return s.repo.ListTeams(ctx) }
func (s *Service) UpdateTeam(ctx context.Context, id, name, phone string) error {
	return s.repo.UpdateTeam(ctx, id, name, phone)
}
func (s *Service) DeactivateTeam(ctx context.Context, id string) error {
	return s.repo.DeactivateTeam(ctx, id)
}
func (s *Service) SetLocksmithTeam(ctx context.Context, slot int, teamID *string) error {
	return s.repo.SetLocksmithTeam(ctx, slot, teamID)
}
func (s *Service) GetLocksmithAssignments(ctx context.Context) ([]*LocksmithTeamAssignment, error) {
	return s.repo.GetLocksmithAssignments(ctx)
}

// ── Handler ──────────────────────────────────────────────────

func (h *Handler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	var in CreateTeamInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	userID, _ := r.Context().Value(middleware.UserIDKey).(string)
	t, err := h.svc.CreateTeam(r.Context(), &in, userID)
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 201, t)
}

func (h *Handler) ListTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := h.svc.ListTeams(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, teams)
}

func (h *Handler) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	var in CreateTeamInput
	if err := decode(r, &in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	if err := h.svc.UpdateTeam(r.Context(), chi.URLParam(r, "id"), in.Name, in.Phone); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}

func (h *Handler) DeactivateTeam(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeactivateTeam(r.Context(), chi.URLParam(r, "id")); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "deaktiviert"})
}

func (h *Handler) GetLocksmithAssignments(w http.ResponseWriter, r *http.Request) {
	assignments, err := h.svc.GetLocksmithAssignments(r.Context())
	if err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, assignments)
}

func (h *Handler) SetLocksmithAssignment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TeamID *string `json:"team_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		response.Error(w, 400, "ungültige eingabe")
		return
	}
	slotNum, err := strconv.Atoi(chi.URLParam(r, "slot"))
	if err != nil || (slotNum != 1 && slotNum != 2) {
		response.Error(w, 400, "ungültiger slot (nur 1 oder 2)")
		return
	}
	if err := h.svc.SetLocksmithTeam(r.Context(), slotNum, in.TeamID); err != nil {
		response.Error(w, 500, err.Error())
		return
	}
	response.JSON(w, 200, map[string]string{"status": "gespeichert"})
}
