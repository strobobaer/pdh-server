package rbac

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"pdh/pkg/middleware"
	"pdh/pkg/response"
)

// ── Typen ────────────────────────────────────────────────────

// Level bildet die Hierarchie der Rollen ab: je hoeher die Zahl, desto
// ranghoeher die Rolle. Eine Rolle darf nur Benutzer und Rollen mit
// echt niedrigerem Level verwalten - die ranghoechste Rolle im System
// ist davon ausgenommen und darf auch gleichrangige Rollen verwalten.
type Role struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Label     string    `json:"label"`
	IsBuiltin bool      `json:"is_builtin"`
	Level     int       `json:"level"`
	CreatedAt time.Time `json:"created_at"`
}

type Permission struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
}

// ── Repository ───────────────────────────────────────────────

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) ListRoles(ctx context.Context) ([]*Role, error) {
	rows, err := r.db.Query(ctx, `SELECT id, key, label, is_builtin, level, created_at FROM roles ORDER BY level DESC, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Role
	for rows.Next() {
		role := &Role{}
		if err := rows.Scan(&role.ID, &role.Key, &role.Label, &role.IsBuiltin, &role.Level, &role.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, role)
	}
	return list, nil
}

func (r *Repository) GetRoleByID(ctx context.Context, id string) (*Role, error) {
	role := &Role{}
	err := r.db.QueryRow(ctx, `SELECT id, key, label, is_builtin, level, created_at FROM roles WHERE id=$1`, id).
		Scan(&role.ID, &role.Key, &role.Label, &role.IsBuiltin, &role.Level, &role.CreatedAt)
	if err != nil {
		return nil, err
	}
	return role, nil
}

func (r *Repository) CreateRole(ctx context.Context, key, label string, level int) (*Role, error) {
	role := &Role{Key: key, Label: label, Level: level}
	err := r.db.QueryRow(ctx,
		`INSERT INTO roles (key, label, is_builtin, level) VALUES ($1, $2, false, $3) RETURNING id, created_at`,
		key, label, level).Scan(&role.ID, &role.CreatedAt)
	if err != nil {
		return nil, err
	}
	return role, nil
}

// UpdateRoleLevel aendert die Rangstufe einer bestehenden Rolle (auch
// eingebaute Rollen - nur das Loeschen ist auf benutzerdefinierte Rollen
// beschraenkt).
func (r *Repository) UpdateRoleLevel(ctx context.Context, id string, level int) error {
	_, err := r.db.Exec(ctx, `UPDATE roles SET level=$1 WHERE id=$2`, level, id)
	return err
}

// DeleteRole loescht nur benutzerdefinierte Rollen. Eingebaute Rollen
// (admin/manager/technician/worker/viewer) koennen nicht geloescht werden,
// weil zahlreiche RequireRole()-Aufrufe im Code fest darauf verweisen.
func (r *Repository) DeleteRole(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM roles WHERE id=$1 AND is_builtin=false`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("eingebaute Rollen können nicht gelöscht werden")
	}
	return nil
}

func (r *Repository) ListPermissions(ctx context.Context) ([]*Permission, error) {
	rows, err := r.db.Query(ctx, `SELECT id, key, label, category FROM permissions ORDER BY category, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []*Permission
	for rows.Next() {
		p := &Permission{}
		if err := rows.Scan(&p.ID, &p.Key, &p.Label, &p.Category); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, nil
}

// MatrixEntries liefert alle (role_id, permission_id) Paare, die aktuell
// gewaehrt sind - fuer den schnellen Aufbau der Checkbox-Matrix.
func (r *Repository) MatrixEntries(ctx context.Context) (map[string]map[string]bool, error) {
	depts, err := r.MatrixDepartments(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for roleID, perms := range depts {
		out[roleID] = map[string]bool{}
		for permID := range perms {
			out[roleID][permID] = true
		}
	}
	return out, nil
}

// MatrixDepartments: role_id -> permission_id -> Abteilung des Rechts
// ("" = in allen Abteilungen).
func (r *Repository) MatrixDepartments(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := r.db.Query(ctx, `SELECT role_id::text, permission_id::text, COALESCE(department_id::text, '') FROM role_permissions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var roleID, permID, dept string
		if err := rows.Scan(&roleID, &permID, &dept); err != nil {
			return nil, err
		}
		if out[roleID] == nil {
			out[roleID] = map[string]string{}
		}
		out[roleID][permID] = dept
	}
	return out, rows.Err()
}

func (r *Repository) SetRolePermission(ctx context.Context, roleID, permissionID string, granted bool) error {
	if granted {
		_, err := r.db.Exec(ctx,
			`INSERT INTO role_permissions (role_id, permission_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			roleID, permissionID)
		return err
	}
	_, err := r.db.Exec(ctx,
		`DELETE FROM role_permissions WHERE role_id=$1 AND permission_id=$2`, roleID, permissionID)
	return err
}

// SetRolePermissionDepartment vergibt ein Recht an eine Rolle, begrenzt auf
// eine Abteilung samt Unterabteilungen ("" = in allen Abteilungen).
func (r *Repository) SetRolePermissionDepartment(ctx context.Context, roleID, permissionID, departmentID string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_id, department_id) VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid)
		ON CONFLICT (role_id, permission_id) DO UPDATE SET department_id = EXCLUDED.department_id`,
		roleID, permissionID, departmentID)
	return err
}

// RolePermissionKeys liefert die Menge der Permission-Keys, die einer
// Rolle (per key, z.B. "admin") aktuell zugewiesen sind - Grundlage fuer
// den in-memory Cache.
func (r *Repository) RolePermissionKeysByRoleKey(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := r.db.Query(ctx, `
		SELECT ro.key, p.key, COALESCE(rp.department_id::text, '')
		FROM role_permissions rp
		JOIN roles ro ON ro.id = rp.role_id
		JOIN permissions p ON p.id = rp.permission_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var roleKey, permKey, dept string
		if err := rows.Scan(&roleKey, &permKey, &dept); err != nil {
			return nil, err
		}
		if out[roleKey] == nil {
			out[roleKey] = map[string]string{}
		}
		out[roleKey][permKey] = dept
	}
	return out, rows.Err()
}

// RoleLevelsByKey liefert die Rangstufe je Rollen-Key - Grundlage fuer
// den in-memory Hierarchie-Cache.
func (r *Repository) RoleLevelsByKey(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.Query(ctx, `SELECT key, level FROM roles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var level int
		if err := rows.Scan(&key, &level); err != nil {
			return nil, err
		}
		out[key] = level
	}
	return out, nil
}

// UserPermissionOverridesByUserID liefert alle Einzelberechtigungs-
// Abweichungen (userID -> permissionKey -> granted) - Grundlage fuer den
// in-memory Override-Cache. true = zusaetzlich erlaubt, false = trotz
// Rolle entzogen.
func (r *Repository) UserPermissionOverridesByUserID(ctx context.Context) (map[string]map[string]Override, error) {
	rows, err := r.db.Query(ctx, `
		SELECT up.user_id::text, p.key, up.granted, COALESCE(up.department_id::text, '')
		FROM user_permissions up
		JOIN permissions p ON p.id = up.permission_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]Override{}
	for rows.Next() {
		var userID, permKey, dept string
		var granted bool
		if err := rows.Scan(&userID, &permKey, &granted, &dept); err != nil {
			return nil, err
		}
		if out[userID] == nil {
			out[userID] = map[string]Override{}
		}
		out[userID][permKey] = Override{Granted: granted, Department: dept}
	}
	return out, rows.Err()
}

// UserPermissionDepartments: permission_id -> Abteilung einer Einzel-Erlaubnis
// ("" = in allen Abteilungen) – für die Bearbeitungsansicht.
func (r *Repository) UserPermissionDepartments(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := r.db.Query(ctx, `SELECT permission_id::text, COALESCE(department_id::text, '') FROM user_permissions WHERE user_id=$1::uuid AND granted`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var permID, dept string
		if err := rows.Scan(&permID, &dept); err != nil {
			return nil, err
		}
		out[permID] = dept
	}
	return out, rows.Err()
}

// DepartmentParents: Abteilung -> übergeordnete Abteilung ("" = oberste Ebene).
func (r *Repository) DepartmentParents(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.Query(ctx, `SELECT id::text, COALESCE(parent_id::text, '') FROM departments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, parent string
		if err := rows.Scan(&id, &parent); err != nil {
			return nil, err
		}
		out[id] = parent
	}
	return out, rows.Err()
}

// UserPermissionOverrides liefert die Einzelberechtigungs-Abweichungen
// eines einzelnen Benutzers (permissionID -> granted) - fuer die
// Bearbeitungsansicht.
func (r *Repository) UserPermissionOverrides(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := r.db.Query(ctx, `SELECT permission_id::text, granted FROM user_permissions WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var permID string
		var granted bool
		if err := rows.Scan(&permID, &granted); err != nil {
			return nil, err
		}
		out[permID] = granted
	}
	return out, nil
}

// SetUserPermission setzt eine Einzelberechtigungs-Abweichung fuer einen
// Benutzer (erlauben oder entziehen, unabhaengig vom Rollen-Standard).
func (r *Repository) SetUserPermission(ctx context.Context, userID, permissionID string, granted bool) error {
	return r.SetUserPermissionDepartment(ctx, userID, permissionID, granted, "")
}

// SetUserPermissionDepartment: wie SetUserPermission, eine Erlaubnis kann auf
// eine Abteilung samt Unterabteilungen begrenzt sein ("" = alle). Ein Verbot
// gilt immer überall.
func (r *Repository) SetUserPermissionDepartment(ctx context.Context, userID, permissionID string, granted bool, departmentID string) error {
	if !granted {
		departmentID = ""
	}
	_, err := r.db.Exec(ctx, `
		INSERT INTO user_permissions (user_id, permission_id, granted, department_id, updated_at)
		VALUES ($1::uuid, $2::uuid, $3, NULLIF($4, '')::uuid, NOW())
		ON CONFLICT (user_id, permission_id) DO UPDATE SET granted=EXCLUDED.granted, department_id=EXCLUDED.department_id, updated_at=NOW()`,
		userID, permissionID, granted, departmentID)
	return err
}

// ClearUserPermission entfernt eine Einzelberechtigungs-Abweichung - der
// Benutzer faellt fuer diese Berechtigung wieder auf den Rollen-Standard
// zurueck.
func (r *Repository) ClearUserPermission(ctx context.Context, userID, permissionID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM user_permissions WHERE user_id=$1::uuid AND permission_id=$2::uuid`, userID, permissionID)
	return err
}

func (r *Repository) GetSetting(ctx context.Context, key string) (string, error) {
	var value string
	err := r.db.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&value)
	return value, err
}

func (r *Repository) SetSetting(ctx context.Context, key, value string) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value=$2, updated_at=NOW()`, key, value)
	return err
}

// ── Service (mit In-Memory-Cache für die heiße Berechtigungsprüfung) ──

type Service struct {
	repo *Repository

	mu               sync.RWMutex
	permCache        map[string]map[string]string   // roleKey -> permissionKey -> Abteilung ("" = alle)
	userOverrides    map[string]map[string]Override // userID -> permissionKey -> Allow/Deny (+ Abteilung)
	deptParent       map[string]string              // Abteilung -> übergeordnete Abteilung
	roleLevels       map[string]int                 // roleKey -> Rangstufe
	maxRoleLevel     int
	idleTimeoutMin   int
	overrideTimeoutM int
}

func NewService(repo *Repository) *Service {
	s := &Service{
		repo: repo, permCache: map[string]map[string]string{}, userOverrides: map[string]map[string]Override{}, deptParent: map[string]string{},
		roleLevels: map[string]int{}, idleTimeoutMin: 30, overrideTimeoutM: 10,
	}
	return s
}

// Warm laedt den Berechtigungs-Cache und die Einstellungen initial (beim
// Serverstart aufzurufen).
func (s *Service) Warm(ctx context.Context) error {
	return s.RefreshCache(ctx)
}

func (s *Service) RefreshCache(ctx context.Context) error {
	m, err := s.repo.RolePermissionKeysByRoleKey(ctx)
	if err != nil {
		return err
	}
	levels, err := s.repo.RoleLevelsByKey(ctx)
	if err != nil {
		return err
	}
	overrides, err := s.repo.UserPermissionOverridesByUserID(ctx)
	if err != nil {
		return err
	}
	parents, err := s.repo.DepartmentParents(ctx)
	if err != nil {
		return err
	}
	maxLevel := 0
	for _, level := range levels {
		if level > maxLevel {
			maxLevel = level
		}
	}
	idleStr, err := s.repo.GetSetting(ctx, "idle_timeout_minutes")
	idleMin := 30
	if err == nil {
		if v, convErr := strconv.Atoi(idleStr); convErr == nil && v > 0 {
			idleMin = v
		}
	}
	overrideStr, err := s.repo.GetSetting(ctx, "override_timeout_minutes")
	overrideMin := 10
	if err == nil {
		if v, convErr := strconv.Atoi(overrideStr); convErr == nil && v > 0 {
			overrideMin = v
		}
	}

	s.mu.Lock()
	s.permCache = m
	s.userOverrides = overrides
	s.deptParent = parents
	s.roleLevels = levels
	s.maxRoleLevel = maxLevel
	s.idleTimeoutMin = idleMin
	s.overrideTimeoutM = overrideMin
	s.mu.Unlock()
	return nil
}

// HasPermission prüft, ob eine Rolle (per key, z.B. "admin") eine
// bestimmte Berechtigung hat. Nutzt ausschließlich den In-Memory-Cache,
// damit die Prüfung auch bei jedem Request schnell bleibt.
func (s *Service) HasPermission(roleKey, permissionKey string) bool {
	all, depts := s.Scope("", roleKey, permissionKey)
	return all || len(depts) > 0
}

// HasPermissionForUser prueft eine Berechtigung fuer einen konkreten
// Benutzer: eine Einzelberechtigungs-Abweichung (Allow/Deny) hat immer
// Vorrang vor der Rollen-Matrix; ohne Abweichung gilt die Rolle wie
// gewohnt (HasPermission).
func (s *Service) HasPermissionForUser(userID, roleKey, permissionKey string) bool {
	all, depts := s.Scope(userID, roleKey, permissionKey)
	return all || len(depts) > 0
}

// RoleLevel liefert die Rangstufe einer Rolle (per key). Unbekannte
// Rollen gelten als Rang 0 (niedrigste Stufe).
func (s *Service) RoleLevel(roleKey string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.roleLevels[roleKey]
}

// Outranks prueft, ob actorRoleKey targetRoleKey in der Hierarchie
// verwalten darf: echt hoeherer Rang, oder actorRoleKey ist die
// ranghoechste Rolle im System (die darf auch Gleichrangige verwalten).
func (s *Service) Outranks(actorRoleKey, targetRoleKey string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	actorLevel := s.roleLevels[actorRoleKey]
	if actorLevel > 0 && actorLevel >= s.maxRoleLevel {
		return true
	}
	return actorLevel > s.roleLevels[targetRoleKey]
}

// MaxRoleLevel liefert die hoechste aktuell vergebene Rangstufe -
// Grundlage, um zu erkennen, ob eine Rolle ranghoechst ist.
func (s *Service) MaxRoleLevel() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxRoleLevel
}

func (s *Service) IdleTimeoutMinutes() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.idleTimeoutMin
}

func (s *Service) OverrideTimeoutMinutes() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.overrideTimeoutM
}

func (s *Service) ListRoles(ctx context.Context) ([]*Role, error) { return s.repo.ListRoles(ctx) }
func (s *Service) GetRoleByID(ctx context.Context, id string) (*Role, error) {
	return s.repo.GetRoleByID(ctx, id)
}
func (s *Service) ListPermissions(ctx context.Context) ([]*Permission, error) {
	return s.repo.ListPermissions(ctx)
}
func (s *Service) MatrixEntries(ctx context.Context) (map[string]map[string]bool, error) {
	return s.repo.MatrixEntries(ctx)
}

// RequirePermission ist eine chi-kompatible Middleware fuer die
// API-Routen (middleware.Auth setzt den Rollen-Context, hier draufsetzen).
// Fuer Web-Seiten (HTML) wird die Berechtigung stattdessen direkt per
// HasPermission() im jeweiligen Handler geprueft, weil dort statt eines
// Redirects meist eine sprechendere Fehlermeldung/Weiterleitung noetig ist.
func (s *Service) RequirePermission(permissionKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			roleKey, _ := r.Context().Value(middleware.RoleKey).(string)
			userID, _ := r.Context().Value(middleware.UserIDKey).(string)
			if !s.HasPermissionForUser(userID, roleKey, permissionKey) {
				response.Error(w, http.StatusForbidden, "keine berechtigung")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Service) CreateRole(ctx context.Context, key, label string, level int) (*Role, error) {
	role, err := s.repo.CreateRole(ctx, key, label, level)
	if err != nil {
		return nil, err
	}
	_ = s.RefreshCache(ctx)
	return role, nil
}

func (s *Service) UpdateRoleLevel(ctx context.Context, id string, level int) error {
	if err := s.repo.UpdateRoleLevel(ctx, id, level); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

func (s *Service) DeleteRole(ctx context.Context, id string) error {
	if err := s.repo.DeleteRole(ctx, id); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

func (s *Service) SetRolePermission(ctx context.Context, roleID, permissionID string, granted bool) error {
	if err := s.repo.SetRolePermission(ctx, roleID, permissionID, granted); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

// SetRolePermissionDepartment: Recht vergeben und auf eine Abteilung begrenzen ("" = alle).
func (s *Service) SetRolePermissionDepartment(ctx context.Context, roleID, permissionID, departmentID string) error {
	if err := s.repo.SetRolePermissionDepartment(ctx, roleID, permissionID, departmentID); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

// MatrixDepartments: role_id -> permission_id -> Abteilung ("" = alle).
func (s *Service) MatrixDepartments(ctx context.Context) (map[string]map[string]string, error) {
	return s.repo.MatrixDepartments(ctx)
}

// UserPermissionDepartments: permission_id -> Abteilung einer Einzel-Erlaubnis.
func (s *Service) UserPermissionDepartments(ctx context.Context, userID string) (map[string]string, error) {
	return s.repo.UserPermissionDepartments(ctx, userID)
}

// SetUserPermissionDepartment: Einzel-Erlaubnis mit Abteilung ("" = alle).
func (s *Service) SetUserPermissionDepartment(ctx context.Context, userID, permissionID string, granted bool, departmentID string) error {
	if err := s.repo.SetUserPermissionDepartment(ctx, userID, permissionID, granted, departmentID); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

func (s *Service) UserPermissionOverrides(ctx context.Context, userID string) (map[string]bool, error) {
	return s.repo.UserPermissionOverrides(ctx, userID)
}

func (s *Service) SetUserPermission(ctx context.Context, userID, permissionID string, granted bool) error {
	if err := s.repo.SetUserPermission(ctx, userID, permissionID, granted); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

func (s *Service) ClearUserPermission(ctx context.Context, userID, permissionID string) error {
	if err := s.repo.ClearUserPermission(ctx, userID, permissionID); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

func (s *Service) SetIdleTimeoutMinutes(ctx context.Context, minutes int) error {
	if minutes < 1 {
		minutes = 1
	}
	if err := s.repo.SetSetting(ctx, "idle_timeout_minutes", strconv.Itoa(minutes)); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}

func (s *Service) SetOverrideTimeoutMinutes(ctx context.Context, minutes int) error {
	if minutes < 1 {
		minutes = 1
	}
	if err := s.repo.SetSetting(ctx, "override_timeout_minutes", strconv.Itoa(minutes)); err != nil {
		return err
	}
	return s.RefreshCache(ctx)
}
