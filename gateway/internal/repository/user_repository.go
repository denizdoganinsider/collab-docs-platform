package repository

import (
	"database/sql"
	"errors"

	"collab-docs-platform/gateway/internal/domain"

	"github.com/go-sql-driver/mysql"
)

// ErrDuplicateEmail is the unique index on users.email speaking: the loser
// of two concurrent registrations of the same address lands here, not on the
// service's read-then-insert check.
var ErrDuplicateEmail = errors.New("duplicate email")

const mysqlDuplicateEntry = 1062

type UserRepository struct {
	db *sql.DB
}

func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) Create(user *domain.User) error {
	query := `
	INSERT INTO users (email, password_hash, role)
	VALUES (?, ?, ?)
	`

	result, err := r.db.Exec(query, user.Email, user.PasswordHash, user.Role)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlDuplicateEntry {
			return ErrDuplicateEmail
		}
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}

	user.ID = id

	// created_at is assigned by the database; read it back so the 201 body
	// carries the real value rather than Go's zero time.
	return r.db.QueryRow(`SELECT created_at FROM users WHERE id = ?`, id).Scan(&user.CreatedAt)
}

func (r *UserRepository) GetByEmail(email string) (*domain.User, error) {
	query := `
	SELECT id, email, password_hash, role, created_at
	FROM users
	WHERE email = ?
	`

	return scanUser(r.db.QueryRow(query, email))
}

func (r *UserRepository) GetByID(id int64) (*domain.User, error) {
	query := `
	SELECT id, email, password_hash, role, created_at
	FROM users
	WHERE id = ?
	`

	return scanUser(r.db.QueryRow(query, id))
}

// List returns one page of users, newest first. The id tiebreak is what makes
// LIMIT/OFFSET deterministic when several users share a created_at second.
func (r *UserRepository) List(limit, offset int) ([]domain.User, error) {
	query := `
	SELECT id, email, password_hash, role, created_at
	FROM users
	ORDER BY created_at DESC, id DESC
	LIMIT ? OFFSET ?
	`

	rows, err := r.db.Query(query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]domain.User, 0)

	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

func (r *UserRepository) Count() (int64, error) {
	var total int64
	err := r.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&total)
	return total, err
}

func scanUser(row *sql.Row) (*domain.User, error) {
	var user domain.User

	err := row.Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.Role,
		&user.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	return &user, nil
}
