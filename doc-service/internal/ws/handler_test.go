package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/middleware"
	"collab-docs-platform/doc-service/internal/service"
	"collab-docs-platform/doc-service/internal/session"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

const (
	testKey   = "test-gateway-key"
	docID     = int64(7)
	maxBytes  = 512
	wait      = 2 * time.Second
	origin    = "http://localhost:9000"
	ownerID   = int64(1)
	editorID  = int64(2)
	viewerID  = int64(3)
	strangerI = int64(4)
)

// ---- Fakes ----------------------------------------------------------------

type fakeStore struct {
	mu  sync.Mutex
	doc *domain.Document
	ops []domain.Op

	// When non-nil, AppendOps blocks until it is closed: the commit is held
	// back so a test can act while an op is applied but not yet acked.
	gate chan struct{}
}

func (f *fakeStore) Load(int64) (*domain.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := *f.doc
	return &d, nil
}

func (f *fakeStore) OpsAfter(_ int64, from int64, limit int) ([]domain.Op, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []domain.Op{}
	for _, op := range f.ops {
		if op.Version > from && len(out) < limit {
			out = append(out, op)
		}
	}
	return out, nil
}

func (f *fakeStore) AppendOps(_ int64, ops []domain.Op) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, ops...)
	return nil
}

func (f *fakeStore) Snapshot(_ int64, content string, version int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if version > f.doc.Version {
		f.doc.Content, f.doc.Version = content, version
	}
	return nil
}

// fakeAccess: roles by user id; anyone else is a stranger.
type fakeAccess struct {
	mu    sync.Mutex
	roles map[int64]string
}

func (a *fakeAccess) Membership(_ int64, userID int64) (string, []domain.Member, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	role, ok := a.roles[userID]
	if !ok {
		return "", nil, service.ErrForbidden
	}
	members := make([]domain.Member, 0, len(a.roles))
	for id, r := range a.roles {
		members = append(members, domain.Member{UserID: id, Role: r})
	}
	return role, members, nil
}

// ---- Harness --------------------------------------------------------------

type harness struct {
	t        *testing.T
	srv      *httptest.Server
	sessions *session.Manager
	store    *fakeStore
	access   *fakeAccess
}

func newHarness(t *testing.T) *harness {
	return newHarnessWith(t, nil)
}

// newHarnessWith takes the store's commit gate up front: the writer
// goroutine descends from this call, so the field is visible to it without
// a lock.
func newHarnessWith(t *testing.T, gate chan struct{}) *harness {
	t.Helper()
	store := &fakeStore{doc: &domain.Document{ID: docID, Title: "Design notes"}, gate: gate}
	access := &fakeAccess{roles: map[int64]string{
		ownerID: domain.RoleOwner, editorID: domain.RoleEditor, viewerID: domain.RoleViewer,
	}}
	lim := session.DefaultLimits()
	lim.MaxCodepoints = 64
	sessions := session.NewManager(store, lim)

	e := echo.New()
	e.Use(middleware.RequestIDMiddleware)
	h := NewHandler(sessions, access, []string{origin}, maxBytes, "doc-test")
	e.GET("/ws", h.Serve, middleware.GatewayAuth(testKey))
	srv := httptest.NewServer(e)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		_ = sessions.Shutdown(ctx)
		srv.Close()
	})
	return &harness{t: t, srv: srv, sessions: sessions, store: store, access: access}
}

func (h *harness) url(doc string) string {
	return "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws?doc=" + doc
}

func headers(userID int64, origin string) http.Header {
	hd := http.Header{}
	hd.Set(middleware.GatewayKeyHeader, testKey)
	hd.Set(middleware.UserIDHeader, fmt.Sprint(userID))
	if origin != "" {
		hd.Set("Origin", origin)
	}
	return hd
}

// dial connects as userID; the HTTP status is returned alongside so a
// refusal can be asserted without a connection.
func (h *harness) dial(userID int64, origin, doc string) (*websocket.Conn, int) {
	h.t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(h.url(doc), headers(userID, origin))
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if err != nil {
		return nil, status
	}
	h.t.Cleanup(func() { conn.Close() })
	return conn, status
}

// connect dials as a member and consumes the snapshot frame.
func (h *harness) connect(userID int64) (*websocket.Conn, frame) {
	h.t.Helper()
	conn, status := h.dial(userID, origin, fmt.Sprint(docID))
	if conn == nil {
		h.t.Fatalf("user %d: dial failed with status %d", userID, status)
	}
	return conn, expect(h.t, conn, session.TypeSnapshot)
}

type frame struct {
	Type    string          `json:"type"`
	V       int64           `json:"v"`
	Seq     int64           `json:"seq"`
	UserID  int64           `json:"user_id"`
	Op      json.RawMessage `json:"op"`
	Content string          `json:"content"`
	Role    string          `json:"role"`
	Title   string          `json:"title"`
	Code    string          `json:"code"`
	Pos     int             `json:"pos"`
	Sel     int             `json:"sel"`
	Users   []struct {
		UserID int64 `json:"user_id"`
	} `json:"users"`
}

// next reads the next non-presence frame.
func next(t *testing.T, conn *websocket.Conn) frame {
	t.Helper()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var f frame
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("frame %s: %v", raw, err)
		}
		if f.Type != session.TypePresence {
			return f
		}
	}
}

func expect(t *testing.T, conn *websocket.Conn, typ string) frame {
	t.Helper()
	f := next(t, conn)
	if f.Type != typ {
		t.Fatalf("got %q frame %+v, want %q", f.Type, f, typ)
	}
	return f
}

func expectClose(t *testing.T, conn *websocket.Conn, code int) {
	t.Helper()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(wait))
		_, raw, err := conn.ReadMessage()
		if err == nil {
			var f frame
			_ = json.Unmarshal(raw, &f)
			if f.Type == session.TypePresence {
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

func send(t *testing.T, conn *websocket.Conn, text string) {
	t.Helper()
	if err := conn.WriteMessage(websocket.TextMessage, []byte(text)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// ---- Upgrade --------------------------------------------------------------

func TestUpgradeRefusals(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name   string
		user   int64
		origin string
		doc    string
		status int
	}{
		{"non-member", strangerI, origin, "7", http.StatusForbidden},
		{"bad doc id", ownerID, origin, "x", http.StatusBadRequest},
		{"zero doc id", ownerID, origin, "0", http.StatusBadRequest},
		{"foreign origin", ownerID, "http://evil.test", "7", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, status := h.dial(tc.user, tc.origin, tc.doc)
			if conn != nil || status != tc.status {
				t.Fatalf("conn=%v status=%d, want refused with %d", conn != nil, status, tc.status)
			}
		})
	}
	if h.sessions.Lookup(docID) != nil {
		t.Fatal("a refused upgrade opened a session")
	}
}

func TestUpgradeWithoutGatewayKeyIs401(t *testing.T) {
	h := newHarness(t)
	hd := http.Header{}
	hd.Set(middleware.UserIDHeader, "1")
	_, resp, err := websocket.DefaultDialer.Dial(h.url("7"), hd)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("err=%v resp=%v, want 401", err, resp)
	}
}

func TestMissingOriginIsAllowed(t *testing.T) {
	h := newHarness(t)
	conn, status := h.dial(ownerID, "", "7")
	if conn == nil {
		t.Fatalf("native client refused with %d", status)
	}
	snap := expect(t, conn, session.TypeSnapshot)
	if snap.Role != domain.RoleOwner || snap.Title != "Design notes" || snap.V != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

// ---- Editing --------------------------------------------------------------

func TestOpIsAckedAndRelayed(t *testing.T) {
	h := newHarness(t)
	a, _ := h.connect(ownerID)
	b, _ := h.connect(editorID)

	send(t, a, `{"type":"op","v":0,"op":["hello"],"seq":1}`)
	ack := expect(t, a, session.TypeAck)
	if ack.V != 1 || ack.Seq != 1 {
		t.Fatalf("ack = %+v", ack)
	}
	op := expect(t, b, session.TypeOp)
	if op.V != 1 || op.UserID != ownerID || string(op.Op) != `["hello"]` {
		t.Fatalf("relayed op = %+v", op)
	}

	// The persisted log has it, and a later joiner sees it in the snapshot.
	if h.store.ops[0].Version != 1 {
		t.Fatalf("log = %+v", h.store.ops)
	}
	c, snap := h.connect(viewerID)
	if snap.Content != "hello" || snap.V != 1 {
		t.Fatalf("late snapshot = %+v", snap)
	}

	// B's op is transformed past nothing (B saw v1) and reaches A and C.
	send(t, b, `{"type":"op","v":1,"op":[5," world"],"seq":1}`)
	expect(t, b, session.TypeAck)
	if f := expect(t, a, session.TypeOp); string(f.Op) != `[5," world"]` {
		t.Fatalf("A got %+v", f)
	}
	if f := expect(t, c, session.TypeOp); f.V != 2 {
		t.Fatalf("C got %+v", f)
	}
	if v, ok := h.sessions.View(docID); !ok || v.Content != "hello world" || v.Version != 2 {
		t.Fatalf("live view = %+v %v", v, ok)
	}
}

func TestViewerCannotEdit(t *testing.T) {
	h := newHarness(t)
	v, _ := h.connect(viewerID)
	send(t, v, `{"type":"op","v":0,"op":["x"],"seq":1}`)
	f := expect(t, v, session.TypeError)
	if f.Code != "forbidden" {
		t.Fatalf("error = %+v", f)
	}
	// Socket stays open: a cursor still goes through.
	o, _ := h.connect(ownerID)
	send(t, v, `{"type":"cursor","pos":0,"sel":0}`)
	if f := expect(t, o, session.TypeCursor); f.UserID != viewerID {
		t.Fatalf("cursor = %+v", f)
	}
}

func TestUnknownFrameTypeIsNonFatal(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(ownerID)
	send(t, c, `{"type":"dance"}`)
	if f := expect(t, c, session.TypeError); f.Code != "bad_frame" {
		t.Fatalf("error = %+v", f)
	}
	send(t, c, `{"type":"ping"}`)
	send(t, c, `{"type":"op","v":0,"op":["ok"],"seq":9}`)
	if f := expect(t, c, session.TypeAck); f.Seq != 9 {
		t.Fatalf("ack = %+v", f)
	}
}

func TestMalformedJSONCloses1003(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(ownerID)
	send(t, c, `{"type":`)
	expectClose(t, c, websocket.CloseUnsupportedData)
}

func TestBinaryFrameCloses1003(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(ownerID)
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	expectClose(t, c, websocket.CloseUnsupportedData)
}

func TestBadOpShapeCloses4400(t *testing.T) {
	h := newHarness(t)
	for _, bad := range []string{
		`{"type":"op","v":0,"op":[1.5],"seq":1}`,
		`{"type":"op","v":0,"op":"hello","seq":1}`,
		`{"type":"op","v":"0","op":["x"],"seq":1}`,
	} {
		c, _ := h.connect(ownerID)
		send(t, c, bad)
		expectClose(t, c, session.CloseProtocol)
	}
}

func TestSecondOpBeforeAckCloses4400(t *testing.T) {
	gate := make(chan struct{})
	h := newHarnessWith(t, gate)
	defer close(gate)
	c, _ := h.connect(ownerID)
	// The commit is held back, so the second op is guaranteed to arrive
	// while the first is applied but not acked: a protocol violation.
	send(t, c, `{"type":"op","v":0,"op":["a"],"seq":1}`)
	send(t, c, `{"type":"op","v":0,"op":["b"],"seq":2}`)
	expectClose(t, c, session.CloseProtocol)
}

func TestFrameOverReadLimitCloses1009(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(ownerID)
	send(t, c, `{"type":"op","v":0,"op":["`+strings.Repeat("x", maxBytes)+`"],"seq":1}`)
	expectClose(t, c, websocket.CloseMessageTooBig)
}

// ---- Events from the HTTP side --------------------------------------------

func TestTitleMembershipAndDeleteEvents(t *testing.T) {
	h := newHarness(t)
	o, _ := h.connect(ownerID)
	e, _ := h.connect(editorID)
	s := h.sessions.Lookup(docID)
	if s == nil {
		t.Fatal("no session")
	}

	s.TitleChanged("Renamed")
	if f := expect(t, o, session.TypeTitle); f.Title != "Renamed" {
		t.Fatalf("title = %+v", f)
	}
	expect(t, e, session.TypeTitle)

	// Downgrade: socket stays, edits refused.
	s.MemberChanged(editorID, domain.RoleViewer)
	send(t, e, `{"type":"op","v":0,"op":["x"],"seq":1}`)
	if f := expect(t, e, session.TypeError); f.Code != "forbidden" {
		t.Fatalf("error = %+v", f)
	}

	// Removal: 4003.
	s.MemberChanged(editorID, "")
	expectClose(t, e, session.CloseRevoked)

	// Delete: 4004, session gone.
	s.Deleted()
	expectClose(t, o, session.CloseDeleted)
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("session did not end after delete")
	}
	if h.sessions.Lookup(docID) != nil {
		t.Fatal("deleted session still registered")
	}
}

func TestShutdownCloses1001(t *testing.T) {
	h := newHarness(t)
	c, _ := h.connect(ownerID)
	send(t, c, `{"type":"op","v":0,"op":["kept"],"seq":1}`)
	expect(t, c, session.TypeAck)

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := h.sessions.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	expectClose(t, c, websocket.CloseGoingAway)
	if h.store.doc.Content != "kept" {
		t.Fatalf("shutdown snapshot = %q", h.store.doc.Content)
	}
	if _, status := h.dial(ownerID, origin, "7"); status != http.StatusSwitchingProtocols {
		// The upgrade succeeds (the listener is still up) and the join is
		// refused with a close frame.
		t.Fatalf("post-shutdown dial status = %d", status)
	}
}

func TestClientLeaveIsNoticed(t *testing.T) {
	h := newHarness(t)
	a, _ := h.connect(ownerID)
	b, _ := h.connect(editorID)
	// Drain the presence frame B's join sent to A, then close B.
	_ = a.SetReadDeadline(time.Now().Add(wait))
	_, raw, err := a.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var f frame
	_ = json.Unmarshal(raw, &f)
	if f.Type != session.TypePresence || len(f.Users) != 2 {
		t.Fatalf("presence after join = %s", raw)
	}

	b.Close()
	_ = a.SetReadDeadline(time.Now().Add(wait))
	_, raw, err = a.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &f)
	if f.Type != session.TypePresence || len(f.Users) != 1 || f.Users[0].UserID != ownerID {
		t.Fatalf("presence after leave = %s", raw)
	}
}
