package domain

import "time"

const (
	RoleOwner  = "owner"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// ValidMemberRole is what an owner may assign to someone else. "owner" is
// never assigned through the members API; it belongs to documents.owner_id.
func ValidMemberRole(role string) bool {
	return role == RoleEditor || role == RoleViewer
}

// CanEdit: owner and editor may change content and title.
func CanEdit(role string) bool {
	return role == RoleOwner || role == RoleEditor
}

type Document struct {
	ID        int64     `json:"id"`
	OwnerID   int64     `json:"owner_id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DocumentSummary is a document as seen in a user's list: no content, plus
// the caller's role in it.
type DocumentSummary struct {
	ID        int64     `json:"id"`
	OwnerID   int64     `json:"owner_id"`
	Title     string    `json:"title"`
	Version   int64     `json:"version"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Member struct {
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
}
