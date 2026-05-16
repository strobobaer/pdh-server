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
	RoleManger     Role = "manager"
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
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreateUserInput - Eingabe für neuen User
type CreateUserInput struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	FirstName  string `json:"first_name"`
	LastName   string `json:"last_name"`
	Role       Role   `json:"role"`
	Department string `json:"department"`
	Phone      string `json:"phone"`
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
		INSERT INTO users (id, username, email, password_hash, first_name, last_name, role, department, phone, active)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, true)
		RETURNING id, created_at, updated_at`
	return r.db.QueryRow(ctx, query,
		u.Username, u.Email, u.PasswordHash,
		u.FirstName, u.LastName, u.Role,
		u.Department, u.Phone,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
}

func (r *Repository) GetByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	query := `SELECT id, username, email, password_hash, first_name, last_name,
		role, department, phone, active, created_at, updated_at
		FROM users WHERE id = $1 AND active = true`
	err := r.db.QueryRow(ctx, query, id).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash,
		&u.FirstName, &u.LastName, &u.Role,
		&u.Department, &u.Phone, &u.Active,
		&u.CreatedAt, &u.UpdatedAt,
	)
	return u, err
}

func (r *Repository) GetByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	query := `SELECT id, username, email, password_hash, first_name, last_name,
		role, department, phone, active, created_at, updated_at
		FROM users WHERE email = $1 AND active = true`
	err := r.db.QueryRow(ctx, query, email).Scan(
		&u.ID, &u.Username, &u.Email, &u.PasswordHash,
		&u.FirstName, &u.LastName, &u.Role,
		&u.Department, &u.Phone, &u.Active,
		&u.CreatedAt, &u.UpdatedAt,
	)
	return u, err
}

func (r *Repository) List(ctx context.Context) ([]*User, error) {
	query := `SELECT id, username, email, first_name, last_name,
		role, department, phone, active, created_at, updated_at
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
			&u.Department, &u.Phone, &u.Active,
			&u.CreatedAt, &u.UpdatedAt,
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
		department=$4, phone=$5, updated_at=NOW()
		WHERE id=$6`
	_, err := r.db.Exec(ctx, query,
		u.FirstName, u.LastName, u.Role,
		u.Department, u.Phone, u.ID,
	)
	return err
}

func (r *Repository) Deactivate(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET active=false, updated_at=NOW() WHERE id=$1`, id)
	return err
}
