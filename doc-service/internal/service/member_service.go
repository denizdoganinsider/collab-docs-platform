package service

import (
	"database/sql"
	"errors"

	"collab-docs-platform/doc-service/internal/domain"
)

type MemberService struct {
	members MemberRepositoryInterface
}

func NewMemberService(members MemberRepositoryInterface) *MemberService {
	return &MemberService{members: members}
}

func (s *MemberService) roleOf(docID, userID int64) (string, error) {
	role, err := s.members.GetRole(docID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrForbidden
	}
	return role, err
}

// List: any member may see who else is in the document.
func (s *MemberService) List(docID, actorID int64) ([]domain.Member, error) {
	if _, err := s.roleOf(docID, actorID); err != nil {
		return nil, err
	}
	return s.members.List(docID)
}

// Set is share and re-role in one: the owner assigns editor or viewer to a
// user id. The id is not checked against users - that table lives in another
// schema, owned by another deployable. The owner's own row is untouchable:
// ownership is documents.owner_id, not a members role.
func (s *MemberService) Set(docID, actorID, targetID int64, role string) (*domain.Member, error) {
	actorRole, err := s.roleOf(docID, actorID)
	if err != nil {
		return nil, err
	}
	if actorRole != domain.RoleOwner {
		return nil, ErrForbidden
	}

	if !domain.ValidMemberRole(role) {
		return nil, invalid("role must be editor or viewer")
	}
	if targetID <= 0 {
		return nil, invalid("invalid user id")
	}
	if targetID == actorID {
		return nil, invalid("the owner's role cannot be changed")
	}

	if err := s.members.Upsert(docID, targetID, role); err != nil {
		return nil, err
	}

	return &domain.Member{UserID: targetID, Role: role}, nil
}

func (s *MemberService) Remove(docID, actorID, targetID int64) error {
	actorRole, err := s.roleOf(docID, actorID)
	if err != nil {
		return err
	}
	if actorRole != domain.RoleOwner {
		return ErrForbidden
	}
	if targetID == actorID {
		return invalid("the owner cannot remove themselves; delete the document instead")
	}

	removed, err := s.members.Delete(docID, targetID)
	if err != nil {
		return err
	}
	if !removed {
		return ErrNotFound
	}
	return nil
}
