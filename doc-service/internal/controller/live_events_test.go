package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"collab-docs-platform/doc-service/internal/middleware"
	"collab-docs-platform/doc-service/internal/repository"
	"collab-docs-platform/doc-service/internal/service"
	"collab-docs-platform/doc-service/internal/session"
	"collab-docs-platform/doc-service/internal/ws"

	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/websocket"
)

// These tests drive the HTTP write and watch a live socket on the same
// router: the controller must tell the open session after the row lands.

const (
	liveOwner  = int64(930001)
	liveMember = int64(930002)
	liveWait   = 2 * time.Second
)

type liveServer struct {
	*client
	sessions *session.Manager
	docID    int64
}

func newLiveServer(t *testing.T) *liveServer {
	t.Helper()
	dsn := os.Getenv("DOCS_TEST_DSN")
	if dsn == "" {
		t.Skip("DOCS_TEST_DSN not set; skipping database-backed live-event tests")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping %s: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })

	docRepo := repository.NewDocumentRepository(db)
	memberRepo := repository.NewMemberRepository(db)
	opRepo := repository.NewOpRepository(db)
	sessions := session.NewManager(repository.NewSessionStore(docRepo, opRepo), session.DefaultLimits())
	docs := service.NewDocumentService(docRepo, memberRepo, opRepo, sessions)
	e := NewRouter(Dependencies{
		GatewayKey: testKey,
		InstanceID: "doc-test",
		Documents:  NewDocumentController(docs, sessions),
		Members:    NewMemberController(service.NewMemberService(memberRepo), sessions),
		WebSocket:  ws.NewHandler(sessions, docs, nil, 65536, "doc-test").Serve,
	})
	srv := httptest.NewServer(e)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sessions.Shutdown(ctx)
		srv.Close()
	})

	c := &client{t: t, url: srv.URL, ops: opRepo}
	status, doc, _ := c.do("POST", "/documents", liveOwner, "user", map[string]string{"title": "Live"})
	if status != http.StatusCreated {
		t.Fatalf("create: status %d body %v", status, doc)
	}
	docID := int64(doc["id"].(float64))
	t.Cleanup(func() { _ = docs.Delete(docID, liveOwner) })
	if status, body, _ := c.do("PUT", fmt.Sprintf("/documents/%d/members/%d", docID, liveMember), liveOwner, "user", map[string]string{"role": "editor"}); status != http.StatusOK {
		t.Fatalf("add editor: status %d body %v", status, body)
	}
	return &liveServer{client: c, sessions: sessions, docID: docID}
}

func (s *liveServer) path(suffix string) string {
	return fmt.Sprintf("/documents/%d%s", s.docID, suffix)
}

// join opens a socket as userID and consumes its snapshot frame.
func (s *liveServer) join(userID int64) *websocket.Conn {
	s.t.Helper()
	hd := http.Header{}
	hd.Set(middleware.GatewayKeyHeader, testKey)
	hd.Set(middleware.UserIDHeader, fmt.Sprint(userID))
	url := "ws" + strings.TrimPrefix(s.url, "http") + "/ws?doc=" + fmt.Sprint(s.docID)
	conn, resp, err := websocket.DefaultDialer.Dial(url, hd)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		s.t.Fatalf("user %d: dial: %v (status %d)", userID, err, status)
	}
	s.t.Cleanup(func() { conn.Close() })
	if f := nextFrame(s.t, conn); f["type"] != session.TypeSnapshot {
		s.t.Fatalf("first frame = %v, want snapshot", f)
	}
	return conn
}

// nextFrame reads the next non-presence frame.
func nextFrame(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(liveWait))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var f map[string]any
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("frame %s: %v", raw, err)
		}
		if f["type"] != session.TypePresence {
			return f
		}
	}
}

func expectCloseCode(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(liveWait))
		_, raw, err := conn.ReadMessage()
		if err == nil {
			if strings.Contains(string(raw), `"type":"`+session.TypePresence+`"`) {
				continue
			}
			t.Fatalf("got frame %s, want close %d", raw, code)
		}
		var ce *websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("read error %v, want close %d", err, code)
		}
		if ce.Code != code {
			t.Fatalf("closed with %d %q, want %d", ce.Code, ce.Text, code)
		}
		return
	}
}

func TestLiveMemberSetDowngradesSocket(t *testing.T) {
	s := newLiveServer(t)
	conn := s.join(liveMember)

	status, body, _ := s.do("PUT", s.path(fmt.Sprintf("/members/%d", liveMember)), liveOwner, "user", map[string]string{"role": "viewer"})
	if status != http.StatusOK {
		t.Fatalf("set viewer: status %d body %v", status, body)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"op","v":0,"op":["x"],"seq":1}`)); err != nil {
		t.Fatal(err)
	}
	f := nextFrame(t, conn)
	if f["type"] != session.TypeError || f["code"] != "forbidden" {
		t.Fatalf("frame after downgrade = %v, want error forbidden", f)
	}
}

func TestLiveMemberRemoveCloses4003(t *testing.T) {
	s := newLiveServer(t)
	conn := s.join(liveMember)

	status, body, _ := s.do("DELETE", s.path(fmt.Sprintf("/members/%d", liveMember)), liveOwner, "user", nil)
	if status != http.StatusNoContent {
		t.Fatalf("remove: status %d body %v", status, body)
	}

	expectCloseCode(t, conn, session.CloseRevoked)
}

func TestLiveRenameSendsTitleFrame(t *testing.T) {
	s := newLiveServer(t)
	conn := s.join(liveMember)

	status, body, _ := s.do("PATCH", s.path(""), liveOwner, "user", map[string]string{"title": "Renamed live"})
	if status != http.StatusOK {
		t.Fatalf("rename: status %d body %v", status, body)
	}

	f := nextFrame(t, conn)
	if f["type"] != session.TypeTitle || f["title"] != "Renamed live" {
		t.Fatalf("frame after rename = %v, want title Renamed live", f)
	}
}

func TestLiveDeleteCloses4004AndEndsSession(t *testing.T) {
	s := newLiveServer(t)
	conn := s.join(liveOwner)
	live := s.sessions.Lookup(s.docID)
	if live == nil {
		t.Fatal("no live session after join")
	}

	status, body, _ := s.do("DELETE", s.path(""), liveOwner, "user", nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: status %d body %v", status, body)
	}

	expectCloseCode(t, conn, session.CloseDeleted)
	select {
	case <-live.Done():
	case <-time.After(liveWait):
		t.Fatal("session did not end after delete")
	}
	if s.sessions.Lookup(s.docID) != nil {
		t.Fatal("deleted document still has a registered session")
	}
}
