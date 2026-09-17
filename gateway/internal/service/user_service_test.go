package service

import (
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"collab-docs-platform/gateway/internal/domain"

	"golang.org/x/crypto/bcrypt"
)

type mockUserRepo struct {
	users  map[string]*domain.User
	nextID int64
}

func newMockUserRepo() *mockUserRepo {
	return &mockUserRepo{users: make(map[string]*domain.User), nextID: 1}
}

func (m *mockUserRepo) Create(user *domain.User) error {
	if _, exists := m.users[user.Email]; exists {
		return errors.New("duplicate email")
	}
	user.ID = m.nextID
	user.CreatedAt = time.Now()
	m.nextID++
	m.users[user.Email] = user
	return nil
}

func (m *mockUserRepo) GetByEmail(email string) (*domain.User, error) {
	user, exists := m.users[email]
	if !exists {
		return nil, sql.ErrNoRows
	}
	return user, nil
}

func (m *mockUserRepo) GetByID(id int64) (*domain.User, error) {
	for _, user := range m.users {
		if user.ID == id {
			return user, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (m *mockUserRepo) List(limit, offset int) ([]domain.User, error) {
	all := make([]domain.User, 0, len(m.users))
	for _, u := range m.users {
		all = append(all, *u)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	if offset >= len(all) {
		return []domain.User{}, nil
	}
	end := min(offset+limit, len(all))
	return all[offset:end], nil
}

func (m *mockUserRepo) Count() (int64, error) {
	return int64(len(m.users)), nil
}

func TestUserService_Register_Success(t *testing.T) {
	svc := NewUserService(newMockUserRepo())

	user, err := svc.Register("test@example.com", "Passw0rd1")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if user.Email != "test@example.com" {
		t.Errorf("user.Email = %q, want test@example.com", user.Email)
	}
	if user.Role != domain.RoleUser {
		t.Errorf("user.Role = %q, want user", user.Role)
	}
	if user.ID == 0 {
		t.Error("user.ID should not be 0")
	}
	if user.PasswordHash == "Passw0rd1" || user.PasswordHash == "" {
		t.Error("password must be stored hashed")
	}
}

func TestUserService_Register_DuplicateEmail(t *testing.T) {
	svc := NewUserService(newMockUserRepo())

	if _, err := svc.Register("test@example.com", "Passw0rd1"); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	_, err := svc.Register("test@example.com", "Passw0rd2")
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("error = %v, want ErrEmailTaken", err)
	}
}

func TestUserService_Register_Invalid(t *testing.T) {
	svc := NewUserService(newMockUserRepo())

	tests := []struct {
		name     string
		email    string
		password string
		wantErr  string
	}{
		{"bad email", "not-an-email", "Passw0rd1", "invalid email format"},
		{"too short", "valid@example.com", "Pass1", "password must be at least 8 characters"},
		{"no uppercase", "valid@example.com", "password1", "password must contain at least one uppercase letter"},
		{"no digit", "valid@example.com", "Passwordd", "password must contain at least one digit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Register(tt.email, tt.password)
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestUserService_Login(t *testing.T) {
	repo := newMockUserRepo()
	svc := NewUserService(repo)

	hash, _ := bcrypt.GenerateFromPassword([]byte("Passw0rd1"), bcrypt.MinCost)
	repo.users["test@example.com"] = &domain.User{ID: 1, Email: "test@example.com", PasswordHash: string(hash), Role: "user"}

	user, err := svc.Login("test@example.com", "Passw0rd1")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if user.ID != 1 {
		t.Errorf("user.ID = %d, want 1", user.ID)
	}

	// Wrong password and unknown user must be indistinguishable.
	for _, tt := range []struct{ email, password string }{
		{"test@example.com", "WrongPassw0rd"},
		{"nobody@example.com", "Passw0rd1"},
	} {
		_, err := svc.Login(tt.email, tt.password)
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("Login(%q) error = %v, want ErrInvalidCredentials", tt.email, err)
		}
	}
}

func TestUserService_ListPage(t *testing.T) {
	repo := newMockUserRepo()
	svc := NewUserService(repo)
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		if _, err := svc.Register(e, "Passw0rd1"); err != nil {
			t.Fatal(err)
		}
	}

	page, err := svc.ListPage(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Users) != 1 || page.Page != 2 || page.PerPage != 2 {
		t.Errorf("page = %+v", page)
	}

	// Out-of-range values are clamped, not rejected.
	page, err = svc.ListPage(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if page.Page != 1 || page.PerPage != MaxPerPage || len(page.Users) != 3 {
		t.Errorf("clamped page = %+v", page)
	}
}
