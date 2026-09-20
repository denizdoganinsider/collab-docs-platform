package controller

import (
	"net/http"

	"collab-docs-platform/doc-service/internal/service"

	"github.com/labstack/echo/v4"
)

type DocumentController struct {
	docs *service.DocumentService
}

func NewDocumentController(docs *service.DocumentService) *DocumentController {
	return &DocumentController{docs: docs}
}

type titleRequest struct {
	Title string `json:"title"`
}

func (dc *DocumentController) Create(c echo.Context) error {
	var req titleRequest
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, "invalid request body")
	}

	userID, _ := currentUser(c)
	doc, err := dc.docs.Create(userID, req.Title)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusCreated, doc)
}

func (dc *DocumentController) List(c echo.Context) error {
	page, perPage, err := paging(c)
	if err != nil {
		return respondError(c, err)
	}

	userID, _ := currentUser(c)
	docs, err := dc.docs.List(userID, page, perPage)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusOK, docs)
}

func (dc *DocumentController) Get(c echo.Context) error {
	docID, ok := pathID(c, "id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid document id")
	}

	userID, _ := currentUser(c)
	view, err := dc.docs.Get(docID, userID)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusOK, view)
}

// Rename is PATCH /documents/:id. Only the title: content changes travel
// over the WebSocket (month 2), never through HTTP.
func (dc *DocumentController) Rename(c echo.Context) error {
	docID, ok := pathID(c, "id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid document id")
	}

	var req titleRequest
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, "invalid request body")
	}

	userID, _ := currentUser(c)
	doc, err := dc.docs.Rename(docID, userID, req.Title)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusOK, doc)
}

func (dc *DocumentController) Delete(c echo.Context) error {
	docID, ok := pathID(c, "id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid document id")
	}

	userID, _ := currentUser(c)
	if err := dc.docs.Delete(docID, userID); err != nil {
		return respondError(c, err)
	}

	return c.NoContent(http.StatusNoContent)
}

func (dc *DocumentController) ListAll(c echo.Context) error {
	page, perPage, err := paging(c)
	if err != nil {
		return respondError(c, err)
	}

	result, err := dc.docs.ListAll(page, perPage)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusOK, result)
}
