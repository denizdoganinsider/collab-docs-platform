package repository

import (
	"database/sql"

	"collab-docs-platform/doc-service/internal/domain"
)

type DocumentRepository struct {
	db *sql.DB
}

func NewDocumentRepository(db *sql.DB) *DocumentRepository {
	return &DocumentRepository{db: db}
}

const documentColumns = `id, owner_id, title, content, version, created_at, updated_at`

// Create inserts the document and its owner's membership row in ONE
// transaction. A document without an owner row would be invisible to every
// permission check, including its creator's.
func (r *DocumentRepository) Create(doc *domain.Document) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO documents (owner_id, title, content, version) VALUES (?, ?, '', 0)`,
		doc.OwnerID, doc.Title,
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(
		`INSERT INTO document_members (doc_id, user_id, role) VALUES (?, ?, ?)`,
		id, doc.OwnerID, domain.RoleOwner,
	); err != nil {
		return err
	}

	if err := tx.QueryRow(`SELECT `+documentColumns+` FROM documents WHERE id = ?`, id).Scan(
		&doc.ID, &doc.OwnerID, &doc.Title, &doc.Content, &doc.Version, &doc.CreatedAt, &doc.UpdatedAt,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *DocumentRepository) GetByID(id int64) (*domain.Document, error) {
	var doc domain.Document
	err := r.db.QueryRow(`SELECT `+documentColumns+` FROM documents WHERE id = ?`, id).Scan(
		&doc.ID, &doc.OwnerID, &doc.Title, &doc.Content, &doc.Version, &doc.CreatedAt, &doc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// ListForUser: documents the user is a member of, newest activity first.
// DATETIME(3) plus the id tiebreak keeps LIMIT/OFFSET deterministic.
func (r *DocumentRepository) ListForUser(userID int64, limit, offset int) ([]domain.DocumentSummary, error) {
	rows, err := r.db.Query(`
		SELECT d.id, d.owner_id, d.title, d.version, m.role, d.created_at, d.updated_at
		FROM document_members m
		JOIN documents d ON d.id = m.doc_id
		WHERE m.user_id = ?
		ORDER BY d.updated_at DESC, d.id DESC
		LIMIT ? OFFSET ?`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSummaries(rows)
}

// ListAll is the admin view: every document, the role column reporting the
// owner (the only role that is a property of the document itself).
func (r *DocumentRepository) ListAll(limit, offset int) ([]domain.DocumentSummary, error) {
	rows, err := r.db.Query(`
		SELECT id, owner_id, title, version, 'owner', created_at, updated_at
		FROM documents
		ORDER BY updated_at DESC, id DESC
		LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanSummaries(rows)
}

func (r *DocumentRepository) CountAll() (int64, error) {
	var total int64
	err := r.db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&total)
	return total, err
}

func (r *DocumentRepository) UpdateTitle(id int64, title string) error {
	_, err := r.db.Exec(`UPDATE documents SET title = ? WHERE id = ?`, title, id)
	return err
}

// Delete removes the document, its op log and its members in one
// transaction. There are no foreign keys to cascade for us (see schema).
func (r *DocumentRepository) Delete(id int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM document_ops WHERE doc_id = ?`,
		`DELETE FROM document_members WHERE doc_id = ?`,
		`DELETE FROM documents WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func scanSummaries(rows *sql.Rows) ([]domain.DocumentSummary, error) {
	docs := make([]domain.DocumentSummary, 0)
	for rows.Next() {
		var d domain.DocumentSummary
		if err := rows.Scan(&d.ID, &d.OwnerID, &d.Title, &d.Version, &d.Role, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}
