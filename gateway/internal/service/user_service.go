package service

import (
	"database/sql"
	"errors"

	"collab-docs-platform/gateway/internal/domain"
	"collab-docs-platform/gateway/internal/validation"

	"golang.org/x/crypto/bcrypt"
)

type UserRepositoryInterface interface {
	Create(user *domain.User) error
	GetByEmail(email string) (*domain.User, error)
	GetByID(id int64) (*domain.User, error)
	List(limit, offset int) ([]domain.User, error)
	Count() (int64, error)
}

// ErrInvalidCredentials is the single answer for "no such user" and "wrong
// password": distinguishing them would be an account-enumeration oracle.
var ErrInvalidCredentials = errors.New("invalid email or password")

var ErrEmailTaken = errors.New("email already exists")

type UserService struct {
	userRepo UserRepositoryInterface
}

func NewUserService(userRepo UserRepositoryInterface) *UserService {
	return &UserService{userRepo: userRepo}
}

func (s *UserService) Register(email, password string) (*domain.User, error) {
	if err := validation.ValidateEmail(email); err != nil {
		return nil, err
	}

	if err := validation.ValidatePassword(password); err != nil {
		return nil, err
	}

	existing, err := s.userRepo.GetByEmail(email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if existing != nil {
		return nil, ErrEmailTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	user := &domain.User{
		Email:        email,
		PasswordHash: string(hash),
		Role:         domain.RoleUser,
	}

	if err := s.userRepo.Create(user); err != nil {
		return nil, err
	}

	return user, nil
}

func (s *UserService) Login(email, password string) (*domain.User, error) {
	user, err := s.userRepo.GetByEmail(email)
	if err != nil {
		return nil, ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}

func (s *UserService) GetByID(userID int64) (*domain.User, error) {
	return s.userRepo.GetByID(userID)
}

type UserPage struct {
	Users   []domain.User `json:"users"`
	Page    int           `json:"page"`
	PerPage int           `json:"per_page"`
	Total   int64         `json:"total"`
}

const (
	DefaultPerPage = 20
	MaxPerPage     = 100
)

// ListPage clamps rather than rejects out-of-range paging values: a wrong
// per_page is a client mistake, not a reason to fail the request.
func (s *UserService) ListPage(page, perPage int) (*UserPage, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = DefaultPerPage
	}
	if perPage > MaxPerPage {
		perPage = MaxPerPage
	}

	users, err := s.userRepo.List(perPage, (page-1)*perPage)
	if err != nil {
		return nil, err
	}

	total, err := s.userRepo.Count()
	if err != nil {
		return nil, err
	}

	return &UserPage{Users: users, Page: page, PerPage: perPage, Total: total}, nil
}
