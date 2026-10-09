package ws

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/middleware"
	"collab-docs-platform/doc-service/internal/session"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// errAccess answers every membership question with a fixed error.
type errAccess struct{ err error }

func (a errAccess) Membership(int64, int64) (string, []domain.Member, error) {
	return "", nil, a.err
}

// loadFailStore passes the membership check's world but fails the open.
type loadFailStore struct {
	*fakeStore
	err error
}

func (s *loadFailStore) Load(int64) (*domain.Document, error) { return nil, s.err }

func serveWith(t *testing.T, store session.Store, access Access) *httptest.Server {
	t.Helper()
	sessions := session.NewManager(store, session.DefaultLimits())
	e := echo.New()
	e.Use(middleware.RequestIDMiddleware)
	e.GET("/ws", NewHandler(sessions, access, []string{origin}, maxBytes, "doc-test").Serve, middleware.GatewayAuth(testKey))
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	return srv
}

func dialServer(t *testing.T, srv *httptest.Server) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?doc=" + fmt.Sprint(docID)
	conn, resp, err := websocket.DefaultDialer.Dial(url, headers(ownerID, origin))
	if conn != nil {
		t.Cleanup(func() { conn.Close() })
	}
	return conn, resp, err
}

func TestMembershipErrorIs500(t *testing.T) {
	store := &fakeStore{doc: &domain.Document{ID: docID}}
	srv := serveWith(t, store, errAccess{err: errors.New("dial tcp: connection refused")})

	conn, resp, err := dialServer(t, srv)
	if conn != nil || err == nil || resp == nil {
		t.Fatalf("conn=%v err=%v resp=%v, want a refused upgrade", conn != nil, err, resp)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil || body["error"] != "internal error" {
		t.Fatalf("body = %s, want {\"error\":\"internal error\"}", raw)
	}
}

func TestJoinOfDeletedDocumentCloses4004(t *testing.T) {
	store := &loadFailStore{fakeStore: &fakeStore{doc: &domain.Document{ID: docID}}, err: sql.ErrNoRows}
	srv := serveWith(t, store, &fakeAccess{roles: map[int64]string{ownerID: domain.RoleOwner}})

	conn, resp, err := dialServer(t, srv)
	if err != nil {
		t.Fatalf("dial: %v (resp %v)", err, resp)
	}
	expectClose(t, conn, session.CloseDeleted)
}

func TestJoinLoadFailureCloses1011(t *testing.T) {
	store := &loadFailStore{fakeStore: &fakeStore{doc: &domain.Document{ID: docID}}, err: errors.New("deadlock found")}
	srv := serveWith(t, store, &fakeAccess{roles: map[int64]string{ownerID: domain.RoleOwner}})

	conn, resp, err := dialServer(t, srv)
	if err != nil {
		t.Fatalf("dial: %v (resp %v)", err, resp)
	}
	expectClose(t, conn, session.CloseServerError)
}
