package repository

import "collab-docs-platform/doc-service/internal/domain"

// SessionStore is the persistence a live document session needs, over the
// two repositories that own the tables.
type SessionStore struct {
	docs *DocumentRepository
	ops  *OpRepository
}

func NewSessionStore(docs *DocumentRepository, ops *OpRepository) *SessionStore {
	return &SessionStore{docs: docs, ops: ops}
}

func (s *SessionStore) Load(docID int64) (*domain.Document, error) {
	return s.docs.GetByID(docID)
}

func (s *SessionStore) OpsAfter(docID, from int64, limit int) ([]domain.Op, error) {
	return s.ops.ListAfter(docID, from, limit)
}

func (s *SessionStore) AppendOps(docID int64, ops []domain.Op) error {
	return s.ops.InsertBatch(docID, ops)
}

func (s *SessionStore) Snapshot(docID int64, content string, version int64) error {
	return s.docs.Snapshot(docID, content, version)
}
