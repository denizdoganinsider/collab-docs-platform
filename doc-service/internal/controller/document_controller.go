package controller

import (
	"net/http"

	"collab-docs-platform/doc-service/internal/service"
	"collab-docs-platform/doc-service/internal/session"

	"github.com/labstack/echo/v4"
)

// Sessions is the live-session registry as the HTTP handlers see it: after a
// write lands in the database, the session open on this instance (if any) is
// told, so sockets learn about a rename, a role change or a delete without
// polling. Hash routing (month 3) guarantees the session is on this instance.
type Sessions interface {
	Lookup(docID int64) *session.Session
}

type DocumentController struct {
	docs     *service.DocumentService
	sessions Sessions
}

func NewDocumentController(docs *service.DocumentService, sessions Sessions) *DocumentController {
	return &DocumentController{docs: docs, sessions: sessions}
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
	if s := dc.sessions.Lookup(docID); s != nil {
		s.TitleChanged(doc.Title)
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
	// Rows first, sockets second: the session closes with 4004 and writes
	// nothing more.
	if s := dc.sessions.Lookup(docID); s != nil {
		s.Deleted()
	}

	return c.NoContent(http.StatusNoContent)
}

// ListOps is GET /documents/:id/ops?from=<v>: the ops after version v,
// ascending, for a client catching up after a reconnect.
func (dc *DocumentController) ListOps(c echo.Context) error {
	docID, ok := pathID(c, "id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid document id")
	}

	from, err := queryInt64(c, "from", 0)
	if err != nil {
		return respondError(c, &service.ValidationError{Msg: "invalid from parameter"})
	}

	userID, _ := currentUser(c)
	ops, err := dc.docs.ListOps(docID, userID, from)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusOK, ops)
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
