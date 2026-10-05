package repository

import (
	"database/sql"

	"collab-docs-platform/doc-service/internal/domain"
)

type OpRepository struct {
	db *sql.DB
}

func NewOpRepository(db *sql.DB) *OpRepository {
	return &OpRepository{db: db}
}

// Insert appends one op. The (doc_id, version) primary key is the ordering
// guarantee: a second writer for the same version gets the driver's
// duplicate-key error back untouched, and the session treats that as
// "reconnect and catch up", never as data to overwrite.
func (r *OpRepository) Insert(docID, version, userID int64, op []byte) error {
	_, err := r.db.Exec(
		`INSERT INTO document_ops (doc_id, version, user_id, op) VALUES (?, ?, ?, ?)`,
		docID, version, userID, op,
	)
	return err
}

// ListAfter returns the ops with version > from, oldest first, at most limit
// rows: the client's catch-up read after a reconnect.
func (r *OpRepository) ListAfter(docID, from int64, limit int) ([]domain.Op, error) {
	rows, err := r.db.Query(`
		SELECT doc_id, version, user_id, op, created_at
		FROM document_ops
		WHERE doc_id = ? AND version > ?
		ORDER BY version ASC
		LIMIT ?`,
		docID, from, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ops := make([]domain.Op, 0)
	for rows.Next() {
		var op domain.Op
		if err := rows.Scan(&op.DocID, &op.Version, &op.UserID, &op.Op, &op.CreatedAt); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}
