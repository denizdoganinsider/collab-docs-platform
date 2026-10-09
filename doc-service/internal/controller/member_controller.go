package controller

import (
	"net/http"

	"collab-docs-platform/doc-service/internal/service"

	"github.com/labstack/echo/v4"
)

type MemberController struct {
	members  *service.MemberService
	sessions Sessions
}

func NewMemberController(members *service.MemberService, sessions Sessions) *MemberController {
	return &MemberController{members: members, sessions: sessions}
}

type roleRequest struct {
	Role string `json:"role"`
}

func (mc *MemberController) List(c echo.Context) error {
	docID, ok := pathID(c, "id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid document id")
	}

	userID, _ := currentUser(c)
	members, err := mc.members.List(docID, userID)
	if err != nil {
		return respondError(c, err)
	}

	return c.JSON(http.StatusOK, members)
}

func (mc *MemberController) Set(c echo.Context) error {
	docID, ok := pathID(c, "id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid document id")
	}
	targetID, ok := pathID(c, "user_id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid user id")
	}

	var req roleRequest
	if err := c.Bind(&req); err != nil {
		return fail(c, http.StatusBadRequest, "invalid request body")
	}

	userID, _ := currentUser(c)
	member, err := mc.members.Set(docID, userID, targetID, req.Role)
	if err != nil {
		return respondError(c, err)
	}
	if s := mc.sessions.Lookup(docID); s != nil {
		s.MemberChanged(member.UserID, member.Role)
	}

	return c.JSON(http.StatusOK, member)
}

func (mc *MemberController) Remove(c echo.Context) error {
	docID, ok := pathID(c, "id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid document id")
	}
	targetID, ok := pathID(c, "user_id")
	if !ok {
		return fail(c, http.StatusBadRequest, "invalid user id")
	}

	userID, _ := currentUser(c)
	if err := mc.members.Remove(docID, userID, targetID); err != nil {
		return respondError(c, err)
	}
	if s := mc.sessions.Lookup(docID); s != nil {
		s.MemberChanged(targetID, "")
	}

	return c.NoContent(http.StatusNoContent)
}
