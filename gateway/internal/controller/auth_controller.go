package controller

import (
	"errors"
	"net/http"

	"collab-docs-platform/gateway/internal/middleware"
	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
)

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthController struct {
	userService *service.UserService
}

func NewAuthController(userService *service.UserService) *AuthController {
	return &AuthController{userService: userService}
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
		if errors.Is(err, service.ErrEmailTaken) {
			return c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
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

	user, err := ac.userService.Login(req.Email, req.Password)
	if err != nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": err.Error()})
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
	if err != nil {
		// A valid token for a deleted user: the credential is real, the subject
		// is gone. 401 makes the client log in again rather than retry.
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "user not found"})
	}

	return c.JSON(http.StatusOK, user)
}
