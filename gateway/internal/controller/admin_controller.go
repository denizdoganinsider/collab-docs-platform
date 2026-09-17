package controller

import (
	"net/http"
	"strconv"

	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
)

type AdminController struct {
	userService *service.UserService
}

func NewAdminController(userService *service.UserService) *AdminController {
	return &AdminController{userService: userService}
}

func (ac *AdminController) ListUsers(c echo.Context) error {
	page, err := queryInt(c, "page", 1)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid page parameter"})
	}

	perPage, err := queryInt(c, "per_page", service.DefaultPerPage)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid per_page parameter"})
	}

	result, err := ac.userService.ListPage(page, perPage)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to fetch users"})
	}

	return c.JSON(http.StatusOK, result)
}

func queryInt(c echo.Context, name string, fallback int) (int, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}
