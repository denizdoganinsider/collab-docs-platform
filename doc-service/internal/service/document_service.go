package service

import (
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"collab-docs-platform/doc-service/internal/domain"
)

type DocumentRepositoryInterface interface {
	Create(doc *domain.Document) error
	GetByID(id int64) (*domain.Document, error)
	ListForUser(userID int64, limit, offset int) ([]domain.DocumentSummary, error)
	ListAll(limit, offset int) ([]domain.DocumentSummary, error)
	CountAll() (int64, error)
	UpdateTitle(id int64, title string) error
	Delete(id int64) error
}

type OpRepositoryInterface interface {
	ListAfter(docID, from int64, limit int) ([]domain.Op, error)
}

type MemberRepositoryInterface interface {
	GetRole(docID, userID int64) (string, error)
	List(docID int64) ([]domain.Member, error)
	Upsert(docID, userID int64, role string) error
	Delete(docID, userID int64) (bool, error)
}

const (
	DefaultPerPage = 20
	MaxPerPage     = 100
	// MaxPage bounds the offset arithmetic: page*per_page must stay far from
	// int64 overflow, which MySQL would reject as a negative OFFSET.
	MaxPage        = 1_000_000
	MaxTitleLength = 255
	// MaxOpsPerPage bounds one catch-up read; a client further behind than
	// this reads again from the last version it received.
	MaxOpsPerPage = 500
)

type DocumentService struct {
	docs    DocumentRepositoryInterface
	members MemberRepositoryInterface
	ops     OpRepositoryInterface
}

func NewDocumentService(docs DocumentRepositoryInterface, members MemberRepositoryInterface, ops OpRepositoryInterface) *DocumentService {
	return &DocumentService{docs: docs, members: members, ops: ops}
}

// DocumentView is GET /documents/:id: the document, the caller's role and
// the member list, in one response.
type DocumentView struct {
	domain.Document
	Role    string          `json:"role"`
	Members []domain.Member `json:"members"`
}

func validateTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", invalid("title is required")
	}
	if utf8.RuneCountInString(title) > MaxTitleLength {
		return "", invalid("title must be at most 255 characters")
	}
	return title, nil
}

func clampPage(page, perPage int) (int, int) {
	if page < 1 {
		page = 1
	}
	if page > MaxPage {
		page = MaxPage
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	if perPage > MaxPerPage {
		perPage = MaxPerPage
	}
	return page, perPage
}

// roleOf turns the membership lookup into the permission answer: a missing
// row is ErrForbidden, everything else is a database problem.
func (s *DocumentService) roleOf(docID, userID int64) (string, error) {
	role, err := s.members.GetRole(docID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	return role, err
}

func (s *DocumentService) Create(ownerID int64, title string) (*domain.DocumentSummary, error) {
	title, err := validateTitle(title)
	if err != nil {
		return nil, err
	}

	doc := &domain.Document{OwnerID: ownerID, Title: title}
	if err := s.docs.Create(doc); err != nil {
		return nil, err
	}

	return &domain.DocumentSummary{
		ID: doc.ID, OwnerID: doc.OwnerID, Title: doc.Title, Version: doc.Version,
		Role: domain.RoleOwner, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt,
	}, nil
}

func (s *DocumentService) List(userID int64, page, perPage int) ([]domain.DocumentSummary, error) {
	page, perPage = clampPage(page, perPage)
	return s.docs.ListForUser(userID, perPage, (page-1)*perPage)
}

func (s *DocumentService) Get(docID, userID int64) (*DocumentView, error) {
	role, err := s.roleOf(docID, userID)
	if err != nil {
		return nil, err
	}

	doc, err := s.docs.GetByID(docID)
	if errors.Is(err, sql.ErrNoRows) {
		// A membership row without its document: only possible mid-delete.
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}

	members, err := s.members.List(docID)
	if err != nil {
		return nil, err
	}

	return &DocumentView{Document: *doc, Role: role, Members: members}, nil
}

func (s *DocumentService) Rename(docID, userID int64, title string) (*domain.DocumentSummary, error) {
	role, err := s.roleOf(docID, userID)
	if err != nil {
		return nil, err
	}
	if !domain.CanEdit(role) {
		return nil, ErrForbidden
	}

	title, err = validateTitle(title)
	if err != nil {
		return nil, err
	}

	if err := s.docs.UpdateTitle(docID, title); err != nil {
		return nil, err
	}

	doc, err := s.docs.GetByID(docID)
	if errors.Is(err, sql.ErrNoRows) {
		// Deleted by the owner between the permission check and the read
		// back: an ordinary race, not a server error.
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}

	return &domain.DocumentSummary{
		ID: doc.ID, OwnerID: doc.OwnerID, Title: doc.Title, Version: doc.Version,
		Role: role, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt,
	}, nil
}

func (s *DocumentService) Delete(docID, userID int64) error {
	role, err := s.roleOf(docID, userID)
	if err != nil {
		return err
	}
	if role != domain.RoleOwner {
		return ErrForbidden
	}
	return s.docs.Delete(docID)
}

// ListOps is the catch-up read: every member (viewers included) may read the
// op log after version `from`, oldest first, at most MaxOpsPerPage rows.
func (s *DocumentService) ListOps(docID, userID, from int64) ([]domain.Op, error) {
	if _, err := s.roleOf(docID, userID); err != nil {
		return nil, err
	}
	if from < 0 {
		return nil, invalid("from must be at least 0")
	}
	return s.ops.ListAfter(docID, from, MaxOpsPerPage)
}

type DocumentPage struct {
	Documents []domain.DocumentSummary `json:"documents"`
	Page      int                      `json:"page"`
	PerPage   int                      `json:"per_page"`
	Total     int64                    `json:"total"`
}

func (s *DocumentService) ListAll(page, perPage int) (*DocumentPage, error) {
	page, perPage = clampPage(page, perPage)

	docs, err := s.docs.ListAll(perPage, (page-1)*perPage)
	if err != nil {
		return nil, err
	}
	total, err := s.docs.CountAll()
	if err != nil {
		return nil, err
	}

	return &DocumentPage{Documents: docs, Page: page, PerPage: perPage, Total: total}, nil
}
