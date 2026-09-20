package controller

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"collab-docs-platform/gateway/internal/domain"
	"collab-docs-platform/gateway/internal/middleware"
	"collab-docs-platform/gateway/internal/repository"
	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

// stubRepo answers every call with a fixed error, or with one stored user.
// createErr, when set, is returned by Create alone (the unique-index race).
type stubRepo struct {
	err       error
	createErr error
	user      *domain.User
}

func (r *stubRepo) Create(*domain.User) error {
	if r.createErr != nil {
		return r.createErr
	}
	return r.err
}
func (r *stubRepo) GetByEmail(string) (*domain.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.user == nil {
		return nil, sql.ErrNoRows
	}
	return r.user, nil
}
func (r *stubRepo) GetByID(int64) (*domain.User, error)  { return r.GetByEmail("") }
func (r *stubRepo) List(int, int) ([]domain.User, error) { return nil, r.err }
func (r *stubRepo) Count() (int64, error)                { return 0, r.err }

func call(t *testing.T, handler echo.HandlerFunc, method, body string, ctx map[string]any) (int, map[string]string) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	for k, v := range ctx {
		c.Set(k, v)
	}
	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func init() { middleware.InitJWT("test") }

// Infrastructure failures are 500 with a bare message, never a 4xx that
// echoes the driver's internals.
func TestAuthController_DatabaseDownIs500(t *testing.T) {
	dbDown := errors.New("dial tcp 127.0.0.1:3308: connect: connection refused")
	ac := NewAuthController(service.NewUserService(&stubRepo{err: dbDown}))

	for _, tt := range []struct {
		name    string
		handler echo.HandlerFunc
		body    string
		ctx     map[string]any
	}{
		{"register", ac.Register, `{"email":"a@x.com","password":"Passw0rd1"}`, nil},
		{"login", ac.Login, `{"email":"a@x.com","password":"Passw0rd1"}`, nil},
		{"me", ac.Me, ``, map[string]any{middleware.UserIDKey: int64(1)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, body := call(t, tt.handler, http.MethodPost, tt.body, tt.ctx)
			if code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", code)
			}
			if body["error"] != "internal error" || strings.Contains(body["error"], "dial tcp") {
				t.Errorf("body = %v, want a bare internal error", body)
			}
		})
	}
}

func TestAuthController_ClientErrors(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("Passw0rd1"), bcrypt.MinCost)
	known := &domain.User{ID: 7, Email: "a@x.com", PasswordHash: string(hash), Role: "user"}

	for _, tt := range []struct {
		name     string
		repo     *stubRepo
		handler  func(*AuthController, echo.Context) error
		body     string
		ctx      map[string]any
		wantCode int
	}{
		{"weak password", &stubRepo{}, (*AuthController).Register, `{"email":"a@x.com","password":"weak"}`, nil, 400},
		{"duplicate via read", &stubRepo{user: known}, (*AuthController).Register, `{"email":"a@x.com","password":"Passw0rd1"}`, nil, 409},
		{"duplicate via unique index", &stubRepo{createErr: repository.ErrDuplicateEmail}, (*AuthController).Register, `{"email":"a@x.com","password":"Passw0rd1"}`, nil, 409},
		{"unknown user", &stubRepo{}, (*AuthController).Login, `{"email":"a@x.com","password":"Passw0rd1"}`, nil, 401},
		{"wrong password", &stubRepo{user: known}, (*AuthController).Login, `{"email":"a@x.com","password":"Wrong0000"}`, nil, 401},
		{"deleted user on /me", &stubRepo{}, (*AuthController).Me, ``, map[string]any{middleware.UserIDKey: int64(1)}, 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ac := NewAuthController(service.NewUserService(tt.repo))
			handler := func(c echo.Context) error { return tt.handler(ac, c) }
			code, _ := call(t, handler, http.MethodPost, tt.body, tt.ctx)
			if code != tt.wantCode {
				t.Errorf("status = %d, want %d", code, tt.wantCode)
			}
		})
	}
}
