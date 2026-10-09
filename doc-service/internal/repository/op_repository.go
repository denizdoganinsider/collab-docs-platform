package repository

import (
	"database/sql"
	"errors"

	"github.com/go-sql-driver/mysql"

	"collab-docs-platform/doc-service/internal/domain"
)

const mysqlDuplicateEntry = 1062

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

// InsertBatch appends ops in one transaction: all of them or none. A
// duplicate (doc_id, version) comes back as domain.ErrVersionTaken so the
// session can tell "someone else wrote this version" from a broken database.
func (r *OpRepository) InsertBatch(docID int64, ops []domain.Op) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO document_ops (doc_id, version, user_id, op) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, op := range ops {
		if _, err := stmt.Exec(docID, op.Version, op.UserID, []byte(op.Op)); err != nil {
			var myErr *mysql.MySQLError
			if errors.As(err, &myErr) && myErr.Number == mysqlDuplicateEntry {
				return domain.ErrVersionTaken
			}
			return err
		}
	}
	return tx.Commit()
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
