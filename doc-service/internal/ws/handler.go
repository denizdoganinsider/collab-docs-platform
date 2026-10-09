// Package ws is the WebSocket edge of a document session: the upgrade
// handler (membership, Origin) and the per-connection read and write pumps.
// Everything about the document itself lives in package session.
package ws

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/middleware"
	"collab-docs-platform/doc-service/internal/service"
	"collab-docs-platform/doc-service/internal/session"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// Access answers the upgrade's permission question: the caller's role in
// the document and its member list. service.DocumentService implements it.
type Access interface {
	Membership(docID, userID int64) (role string, members []domain.Member, err error)
}

type Handler struct {
	sessions   *session.Manager
	access     Access
	upgrader   websocket.Upgrader
	maxBytes   int64
	instanceID string
}

// NewHandler builds the upgrade handler. A missing Origin header is allowed
// and a present one must be in allowedOrigins: cross-site WebSocket
// hijacking is a browser attack, browsers always send Origin, and refusing
// native clients (the probe, websocat) would buy nothing.
func NewHandler(sessions *session.Manager, access Access, allowedOrigins []string, maxBytes int, instanceID string) *Handler {
	return &Handler{
		sessions: sessions,
		access:   access,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				return origin == "" || slices.Contains(allowedOrigins, origin)
			},
		},
		maxBytes:   int64(maxBytes),
		instanceID: instanceID,
	}
}

// Serve handles GET /ws?doc=<id>. Identity comes from the gateway's headers
// (GatewayAuth ran before this); membership is checked here, before the
// upgrade, so a refusal is an HTTP status the client can read. After the
// 101 every answer is a frame or a close code.
func (h *Handler) Serve(c echo.Context) error {
	docID, err := strconv.ParseInt(c.QueryParam("doc"), 10, 64)
	if err != nil || docID <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid document id"})
	}
	userID, _ := c.Get(middleware.UserIDKey).(int64)

	role, members, err := h.access.Membership(docID, userID)
	if errors.Is(err, service.ErrForbidden) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "you do not have access to this document"})
	}
	if err != nil {
		requestID, _ := c.Get(middleware.RequestIDKey).(string)
		slog.Error("websocket membership", "service", "doc-service", "doc_id", docID, "request_id", requestID, "error", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}

	conn, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		// The upgrader has already written the 4xx (bad handshake, Origin).
		return nil
	}
	conn.SetReadLimit(h.maxBytes)

	client := newClient(conn, userID)
	s, err := h.sessions.Join(docID, client, role, members)
	if err != nil {
		code, reason := session.CloseServerError, "internal error"
		switch {
		case errors.Is(err, session.ErrShuttingDown):
			code, reason = session.CloseGoingAway, "shutdown"
		case errors.Is(err, sql.ErrNoRows):
			// Deleted between the membership check and the open.
			code, reason = session.CloseDeleted, "document deleted"
		default:
			slog.Error("websocket join", "service", "doc-service", "doc_id", docID, "error", err)
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
		conn.Close()
		return nil
	}
	client.session = s

	go client.writePump()
	go client.readPump()

	requestID, _ := c.Get(middleware.RequestIDKey).(string)
	slog.Info("websocket connected",
		"service", "doc-service",
		"instance", h.instanceID,
		"doc_id", docID,
		"user_id", userID,
		"role", role,
		"request_id", requestID,
	)
	return nil
}
