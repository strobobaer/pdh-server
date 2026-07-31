package users

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Rollen im System
type Role string

const (
	RoleAdmin      Role = "admin"
	RoleManager    Role = "manager" // FIX: war "RoleManger" (Typo)
	RoleTechnician Role = "technician"
	RoleWorker     Role = "worker"
	RoleViewer     Role = "viewer"
)

// User - Hauptmodell
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	FirstName    string    `json:"first_name"`
	LastName     string    `json:"last_name"`
	Role         Role      `json:"role"`
	Department   string    `json:"department"`
	Phone        string    `json:"phone"`
	Active       bool      `json:"active"`
	IsSystemUser bool      `json:"is_system_user"`
	RFIDUID      *string   `json:"rfid_uid,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// Schicht-Qualifikationen (Kennzeichen, sichtbar im Schichtplan)
	OnCallDuty      bool `json:"on_call_duty"`
	ShiftLocksmith1 bool `json:"shift_locksmith_1"`
	ShiftLocksmith2 bool `json:"shift_locksmith_2"`
	Sharpening      bool `json:"sharpening"`
	HeatingFill     bool `json:"heating_fill"`
	ShiftLeader     bool `json:"shift_leader"`
}

// CreateUserInput - Eingabe für neuen User
type CreateUserInput struct {
	Username     string  `json:"username"`
	Email        string  `json:"email"`
	Password     string  `json:"password"`
	FirstName    string  `json:"first_name"`
	LastName     string  `json:"last_name"`
	Role         Role    `json:"role"`
	Department   string  `json:"department"`
	Phone        string  `json:"phone"`
	IsSystemUser bool    `json:"is_system_user"`
	RFIDUID      *string `json:"rfid_uid,omitempty"`
}

// Repository - Datenbankzugriff
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, u *User) error {
	query := `
		INSERT INTO users (id, username, email, password_hash, first_name, last_name, role, department, phone, active, is_system_user, rfid_uid)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, true, $9, $10)
		RETURNING id, created_at, updated_at`
	return r.db.QueryRow(ctx, query,
		u.Username, u.Email, u.PasswordHash,
		u.FirstName, u.LastName, u.Role,
		u.Department, u.Phone, u.IsSystemUser, u.RFIDUID,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	query := `SELECT id, username, email, password_hash, first_name, last_name,
		role, department, phone, active, is_system_user, rfid_uid, created_at, updated_at,
		on_call_duty, shift_locksmith_1, shift_locksmith_2, sharpening, heating_fill, shift_leader
		FROM users WHERE id = $1 AND active = true`
	err := r.db.QueryRow(ctx, query, id).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash,
		&u.FirstName, &u.LastName, &u.Role,
		&u.Department, &u.Phone, &u.Active, &u.IsSystemUser, &u.RFIDUID,
		&u.CreatedAt, &u.UpdatedAt,
		&u.OnCallDuty, &u.ShiftLocksmith1, &u.ShiftLocksmith2, &u.Sharpening, &u.HeatingFill, &u.ShiftLeader,
	)
	return u, err
}

// GetByIdentifier findet einen Nutzer per E-Mail ODER Benutzername - fürs
// Login-Formular, das beides akzeptiert.
func (r *Repository) GetByIdentifier(ctx context.Context, identifier string) (*User, error) {
	u := &User{}
	query := `SELECT id, username, email, password_hash, first_name, last_name,
		role, department, phone, active, is_system_user, rfid_uid, created_at, updated_at,
		on_call_duty, shift_locksmith_1, shift_locksmith_2, sharpening, heating_fill, shift_leader
		FROM users WHERE (email = $1 OR username = $1) AND active = true`
	err := r.db.QueryRow(ctx, query, identifier).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash,
		&u.FirstName, &u.LastName, &u.Role,
		&u.Department, &u.Phone, &u.Active, &u.IsSystemUser, &u.RFIDUID,
		&u.CreatedAt, &u.UpdatedAt,
		&u.OnCallDuty, &u.ShiftLocksmith1, &u.ShiftLocksmith2, &u.Sharpening, &u.HeatingFill, &u.ShiftLeader,
	)
	return u, err
}

// GetByRFID findet einen Nutzer per RFID-Tag-UID - für die
// Karten-/Transponder-Anmeldung. Der Tag muss (dank UNIQUE-Constraint aus
// der Migration) höchstens einem aktiven Konto zugeordnet sein.
func (r *Repository) GetByRFID(ctx context.Context, uid string) (*User, error) {
	u := &User{}
	query := `SELECT id, username, email, password_hash, first_name, last_name,
		role, department, phone, active, is_system_user, rfid_uid, created_at, updated_at,
		on_call_duty, shift_locksmith_1, shift_locksmith_2, sharpening, heating_fill, shift_leader
		FROM users WHERE rfid_uid = $1 AND active = true`
	err := r.db.QueryRow(ctx, query, uid).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash,
		&u.FirstName, &u.LastName, &u.Role,
		&u.Department, &u.Phone, &u.Active, &u.IsSystemUser, &u.RFIDUID,
		&u.CreatedAt, &u.UpdatedAt,
		&u.OnCallDuty, &u.ShiftLocksmith1, &u.ShiftLocksmith2, &u.Sharpening, &u.HeatingFill, &u.ShiftLeader,
	)
	return u, err
}

func (r *Repository) List(ctx context.Context) ([]*User, error) {
	query := `SELECT id, username, email, first_name, last_name,
		role, department, phone, active, is_system_user, rfid_uid, created_at, updated_at,
		on_call_duty, shift_locksmith_1, shift_locksmith_2, sharpening, heating_fill, shift_leader
		FROM users WHERE active = true ORDER BY last_name, first_name`
	rows, err := r.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		err := rows.Scan(
			&u.ID, &u.Username, &u.Email,
			&u.FirstName, &u.LastName, &u.Role,
			&u.Department, &u.Phone, &u.Active, &u.IsSystemUser, &u.RFIDUID,
			&u.CreatedAt, &u.UpdatedAt,
			&u.OnCallDuty, &u.ShiftLocksmith1, &u.ShiftLocksmith2, &u.Sharpening, &u.HeatingFill, &u.ShiftLeader,
		)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

func (r *Repository) Update(ctx context.Context, u *User) error {
	query := `UPDATE users SET first_name=$1, last_name=$2, role=$3,
		department=$4, phone=$5, is_system_user=$6, rfid_uid=$7,
		on_call_duty=$8, shift_locksmith_1=$9, shift_locksmith_2=$10, sharpening=$11, heating_fill=$12, shift_leader=$13,
		updated_at=NOW()
		WHERE id=$14`
	_, err := r.db.Exec(ctx, query,
		u.FirstName, u.LastName, u.Role,
		u.Department, u.Phone, u.IsSystemUser, u.RFIDUID,
		u.OnCallDuty, u.ShiftLocksmith1, u.ShiftLocksmith2, u.Sharpening, u.HeatingFill, u.ShiftLeader,
		u.ID,
	)
	return err
}

func (r *Repository) Deactivate(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET active=false, updated_at=NOW() WHERE id=$1`, id)
	return err
}

// SetLocksmithSlot setzt genau einen Nutzer als Schichtschlosser 1 oder 2
// (slot=1 oder 2) und setzt das Flag bei allen anderen Nutzern zurueck,
// damit die Rolle immer eindeutig einer Person zugeordnet ist.
func (r *Repository) SetLocksmithSlot(ctx context.Context, slot int, userID string) error {
	column := "shift_locksmith_1"
	if slot == 2 {
		column = "shift_locksmith_2"
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "UPDATE users SET "+column+"=false WHERE "+column+"=true"); err != nil {
		return err
	}
	if userID != "" {
		if _, err := tx.Exec(ctx, "UPDATE users SET "+column+"=true WHERE id=$1", userID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
