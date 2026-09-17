package repository

import (
	"database/sql"

	"collab-docs-platform/doc-service/internal/domain"
)

type MemberRepository struct {
	db *sql.DB
}

func NewMemberRepository(db *sql.DB) *MemberRepository {
	return &MemberRepository{db: db}
}

// GetRole is THE permission check: one lookup in one table. sql.ErrNoRows
// means "not a member", which callers turn into 403.
func (r *MemberRepository) GetRole(docID, userID int64) (string, error) {
	var role string
	err := r.db.QueryRow(
		`SELECT role FROM document_members WHERE doc_id = ? AND user_id = ?`,
		docID, userID,
	).Scan(&role)
	return role, err
}

func (r *MemberRepository) List(docID int64) ([]domain.Member, error) {
	rows, err := r.db.Query(
		`SELECT user_id, role FROM document_members WHERE doc_id = ? ORDER BY created_at, user_id`,
		docID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := make([]domain.Member, 0)
	for rows.Next() {
		var m domain.Member
		if err := rows.Scan(&m.UserID, &m.Role); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (r *MemberRepository) Upsert(docID, userID int64, role string) error {
	_, err := r.db.Exec(`
		INSERT INTO document_members (doc_id, user_id, role) VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE role = VALUES(role)`,
		docID, userID, role,
	)
	return err
}

// Delete reports whether a row was actually removed, so "remove someone who
// was never a member" can be a 404 rather than a silent success.
func (r *MemberRepository) Delete(docID, userID int64) (bool, error) {
	result, err := r.db.Exec(
		`DELETE FROM document_members WHERE doc_id = ? AND user_id = ?`,
		docID, userID,
	)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}
