package ws

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/middleware"
	"collab-docs-platform/doc-service/internal/repository"
	"collab-docs-platform/doc-service/internal/service"
	"collab-docs-platform/doc-service/internal/session"

	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

// The tests in this file run the real stack - repositories, service,
// session, sockets - against the compose MySQL (DOCS_TEST_DSN); they skip
// without it. They are the month's exit criteria that a fake store cannot
// prove: persist-before-ack against a real commit, and the ack latency the
// design notes ask for.

type dbHarness struct {
	t        *testing.T
	srv      *httptest.Server
	sessions *session.Manager
	docs     *service.DocumentService
	docID    int64
}

const (
	dbOwner    = int64(920001)
	dbEditor   = int64(920002)
	dbStranger = int64(920004)
)

func newDBHarness(t *testing.T) *dbHarness {
	t.Helper()
	dsn := os.Getenv("DOCS_TEST_DSN")
	if dsn == "" {
		t.Skip("DOCS_TEST_DSN not set; skipping database-backed WebSocket tests")
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

	created, err := docs.Create(dbOwner, "ws test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docs.Delete(created.ID, dbOwner) })
	if _, err := service.NewMemberService(memberRepo).Set(created.ID, dbOwner, dbEditor, domain.RoleEditor); err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	e.GET("/ws", NewHandler(sessions, docs, nil, 65536, "doc-test").Serve, middleware.GatewayAuth(testKey))
	srv := httptest.NewServer(e)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sessions.Shutdown(ctx)
		srv.Close()
	})
	return &dbHarness{t: t, srv: srv, sessions: sessions, docs: docs, docID: created.ID}
}

func (h *dbHarness) dial(userID int64) (*websocket.Conn, int) {
	h.t.Helper()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/ws?doc=" + fmt.Sprint(h.docID)
	conn, resp, err := websocket.DefaultDialer.Dial(url, headers(userID, ""))
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

func TestDBMembershipAndPersistBeforeAck(t *testing.T) {
	h := newDBHarness(t)

	if conn, status := h.dial(dbStranger); conn != nil || status != http.StatusForbidden {
		t.Fatalf("stranger: conn=%v status=%d, want 403", conn != nil, status)
	}

	owner, _ := h.dial(dbOwner)
	if owner == nil {
		t.Fatal("owner dial failed")
	}
	snap := expect(t, owner, session.TypeSnapshot)
	if snap.Role != domain.RoleOwner || snap.V != 0 || snap.Title != "ws test" {
		t.Fatalf("snapshot = %+v", snap)
	}

	send(t, owner, `{"type":"op","v":0,"op":["hello"],"seq":1}`)
	ack := expect(t, owner, session.TypeAck)

	// The ack arrived: the row must already be committed.
	ops, err := h.docs.ListOps(h.docID, dbOwner, 0)
	if err != nil || len(ops) != 1 || ops[0].Version != ack.V || string(ops[0].Op) != `["hello"]` {
		t.Fatalf("log after ack = %+v (%v)", ops, err)
	}

	// GET /documents/:id answers from the live session: content is "hello"
	// although no snapshot has been written yet.
	view, err := h.docs.Get(h.docID, dbEditor)
	if err != nil || view.Content != "hello" || view.Version != 1 {
		t.Fatalf("live view = %+v (%v)", view, err)
	}
}

// TestDBAckLatency is the design-notes measurement: the ack round trip with
// one client sending sequentially, and with several clients sending at once
// so the writer batches. It records, it does not assert; the numbers are in
// the README.
func TestDBAckLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement skipped with -short")
	}
	h := newDBHarness(t)

	single := func(conn *websocket.Conn, base int64, n int) []time.Duration {
		out := make([]time.Duration, 0, n)
		v := base
		for i := range n {
			start := time.Now()
			send(t, conn, appendOp(v, "x", int64(i+1)))
			ack := expect(t, conn, session.TypeAck)
			out = append(out, time.Since(start))
			v = ack.V
		}
		return out
	}

	owner, _ := h.dial(dbOwner)
	expect(t, owner, session.TypeSnapshot)
	seq := single(owner, 0, 200)
	t.Logf("single client, 200 sequential ops: %s", summary(seq))

	// Concurrent: eight editor sockets (one user may hold several). Each op
	// is sent against the sender's last acked version; the session
	// transforms it past the others. Relayed op frames are skipped.
	const clients, perClient = 8, 50
	var wg sync.WaitGroup
	lat := make([][]time.Duration, clients)
	conns := make([]*websocket.Conn, clients)
	for i := range clients {
		conns[i], _ = h.dial(dbEditor)
		expect(t, conns[i], session.TypeSnapshot)
	}
	start := time.Now()
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn := conns[i]
			v := int64(200)
			for j := range perClient {
				t0 := time.Now()
				send(t, conn, appendOp(v, "y", int64(j+1)))
				for {
					f := next(t, conn)
					if f.Type == session.TypeAck {
						lat[i] = append(lat[i], time.Since(t0))
						v = f.V
						break
					}
					if f.Type == session.TypeOp {
						v = f.V
					}
				}
			}
		}()
	}
	wg.Wait()
	all := make([]time.Duration, 0, clients*perClient)
	for _, l := range lat {
		all = append(all, l...)
	}
	t.Logf("%d clients x %d concurrent ops in %s: %s", clients, perClient, time.Since(start).Round(time.Millisecond), summary(all))

	ops, err := h.docs.ListOps(h.docID, dbOwner, 0)
	if err != nil {
		t.Fatal(err)
	}
	if view, _ := h.sessions.View(h.docID); int(view.Version) != 200+clients*perClient {
		t.Fatalf("version %d, want %d", view.Version, 200+clients*perClient)
	}
	// ListOps pages at 500; the point is that nothing acked is missing.
	if len(ops) != 500 {
		t.Fatalf("first page %d ops, want 500", len(ops))
	}
}

// appendOp inserts one code point at the end of a document that holds one
// code point per version (every op in this test appends exactly one).
func appendOp(v int64, text string, seq int64) string {
	if v == 0 {
		return fmt.Sprintf(`{"type":"op","v":0,"op":[%q],"seq":%d}`, text, seq)
	}
	return fmt.Sprintf(`{"type":"op","v":%d,"op":[%d,%q],"seq":%d}`, v, v, text, seq)
}

func summary(d []time.Duration) string {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	p := func(q float64) time.Duration { return d[int(float64(len(d)-1)*q)].Round(100 * time.Microsecond) }
	return fmt.Sprintf("p50 %s, p90 %s, p99 %s, max %s", p(0.5), p(0.9), p(0.99), p(1))
}
