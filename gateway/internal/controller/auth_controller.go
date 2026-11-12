package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"collab-docs-platform/gateway/internal/middleware"
	"collab-docs-platform/gateway/internal/ratelimit"
	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
)

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthController struct {
	userService *service.UserService
	// Brute-force guard on POST /login, keyed by email and client IP. nil
	// disables it.
	loginLimiter ratelimit.Limiter
}

func NewAuthController(userService *service.UserService, loginLimiter ratelimit.Limiter) *AuthController {
	return &AuthController{userService: userService, loginLimiter: loginLimiter}
}

// loginAllowed counts every attempt, right or wrong, before the password is
// checked. Once the window is spent even the correct password gets 429, so
// the answer never tells a guesser that a guess was right.
func (ac *AuthController) loginAllowed(c echo.Context, email string) (ratelimit.Decision, bool) {
	if ac.loginLimiter == nil {
		return ratelimit.Decision{Allowed: true}, true
	}
	// Hashed so the limiter store never holds an email address.
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + "|" + c.RealIP()))
	ctx, cancel := context.WithTimeout(c.Request().Context(), 250*time.Millisecond)
	defer cancel()
	decision, err := ac.loginLimiter.Allow(ctx, hex.EncodeToString(sum[:]))
	if err != nil {
		slog.Warn("rate limiter unavailable, failing open", "limiter", "login", "error", err)
		return ratelimit.Decision{Allowed: true}, true
	}
	return decision, decision.Allowed
}

func (ac *AuthController) Register(c echo.Context) error {
	var req credentialsRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	if req.Email == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email and password are required"})
	}

	user, err := ac.userService.Register(req.Email, req.Password)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusCreated, user)
}

func (ac *AuthController) Login(c echo.Context) error {
	var req credentialsRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}

	if req.Email == "" || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "email and password are required"})
	}

	if decision, ok := ac.loginAllowed(c, req.Email); !ok {
		return ratelimit.TooManyRequests(c, decision.RetryAfter)
	}

	user, err := ac.userService.Login(req.Email, req.Password)
	if err != nil {
		return respondError(c, err)
	}

	token, err := middleware.GenerateToken(user.ID, user.Role)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
	}

	return c.JSON(http.StatusOK, map[string]string{"token": token})
}

func (ac *AuthController) Me(c echo.Context) error {
	userID, ok := c.Get(middleware.UserIDKey).(int64)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}

	user, err := ac.userService.GetByID(userID)
	if errors.Is(err, sql.ErrNoRows) {
		// A valid token for a deleted user: the credential is real, the subject
		// is gone. 401 makes the client log in again rather than retry.
		return fail(c, http.StatusUnauthorized, "user not found")
	}
	if err != nil {
		// Not a 401: the editor treats 401 as "session over" and drops the
		// token, so a database blip must not log every browser out.
		return respondError(c, err)
	}

	return c.JSON(http.StatusOK, user)
}
