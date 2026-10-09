package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/ot"
)

const docID = 7

// ---- Fakes ----------------------------------------------------------------

type fakeStore struct {
	mu        sync.Mutex
	doc       *domain.Document
	ops       []domain.Op
	snaps     []snapshot
	batches   []int
	appendErr error
	snapErr   error

	// When non-nil, AppendOps waits for one receive per call.
	gate chan struct{}
}

func newStore(content string, version int64) *fakeStore {
	return &fakeStore{doc: &domain.Document{ID: docID, Title: "Design notes", Content: content, Version: version}}
}

func (f *fakeStore) Load(int64) (*domain.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.doc == nil {
		return nil, sql.ErrNoRows
	}
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
	if f.appendErr != nil {
		return f.appendErr
	}
	f.ops = append(f.ops, ops...)
	f.batches = append(f.batches, len(ops))
	return nil
}

func (f *fakeStore) Snapshot(_ int64, content string, version int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapErr != nil {
		return f.snapErr
	}
	f.snaps = append(f.snaps, snapshot{content: content, version: version})
	if version > f.doc.Version {
		f.doc.Content, f.doc.Version = content, version
	}
	return nil
}

func (f *fakeStore) logLen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ops)
}

func (f *fakeStore) lastSnap() (snapshot, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.snaps) == 0 {
		return snapshot{}, false
	}
	return f.snaps[len(f.snaps)-1], true
}

func (f *fakeStore) seedOp(version int64, op ot.Operation) {
	f.ops = append(f.ops, domain.Op{DocID: docID, Version: version, UserID: 1, Op: encode(op)})
}

type closeInfo struct {
	code   int
	reason string
}

type fakeClient struct {
	id     int64
	frames chan []byte
	closed chan closeInfo
	once   sync.Once
}

func newClient(id int64) *fakeClient { return newClientBuf(id, 256) }

func newClientBuf(id int64, buffer int) *fakeClient {
	return &fakeClient{id: id, frames: make(chan []byte, buffer), closed: make(chan closeInfo, 1)}
}

func (c *fakeClient) UserID() int64 { return c.id }

func (c *fakeClient) Send(frame []byte) bool {
	select {
	case c.frames <- frame:
		return true
	default:
		return false
	}
}

func (c *fakeClient) Close(code int, reason string) {
	c.once.Do(func() { c.closed <- closeInfo{code: code, reason: reason} })
}

// ---- Helpers --------------------------------------------------------------

const wait = 2 * time.Second

func testLimits() Limits {
	return Limits{
		MaxCodepoints:    64,
		RingSize:         8,
		SnapshotEveryOps: 1000,
		SnapshotEvery:    time.Hour,
		Idle:             time.Hour,
		MaxPending:       64,
	}
}

type decoded struct {
	Type     string          `json:"type"`
	V        int64           `json:"v"`
	Seq      int64           `json:"seq"`
	UserID   int64           `json:"user_id"`
	Op       ot.Operation    `json:"op"`
	Content  string          `json:"content"`
	Role     string          `json:"role"`
	Title    string          `json:"title"`
	Code     string          `json:"code"`
	Pos      int             `json:"pos"`
	Sel      int             `json:"sel"`
	Members  []domain.Member `json:"members"`
	Presence []presenceUser  `json:"presence"`
	Users    []presenceUser  `json:"users"`
}

// next returns the client's next frame that is not a presence frame.
func next(t *testing.T, c *fakeClient) decoded {
	t.Helper()
	for {
		d := nextAny(t, c)
		if d.Type != TypePresence {
			return d
		}
	}
}

func nextAny(t *testing.T, c *fakeClient) decoded {
	t.Helper()
	select {
	case raw := <-c.frames:
		var d decoded
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("frame %s: %v", raw, err)
		}
		return d
	case <-time.After(wait):
		t.Fatalf("user %d: no frame", c.id)
		return decoded{}
	}
}

func expect(t *testing.T, c *fakeClient, typ string) decoded {
	t.Helper()
	d := next(t, c)
	if d.Type != typ {
		t.Fatalf("user %d: got %q frame %+v, want %q", c.id, d.Type, d, typ)
	}
	return d
}

// quiet fails if the client receives anything but presence for a short while.
func quiet(t *testing.T, c *fakeClient) {
	t.Helper()
	deadline := time.After(60 * time.Millisecond)
	for {
		select {
		case raw := <-c.frames:
			var d decoded
			_ = json.Unmarshal(raw, &d)
			if d.Type != TypePresence {
				t.Fatalf("user %d: unexpected frame %s", c.id, raw)
			}
		case <-deadline:
			return
		}
	}
}

func expectClose(t *testing.T, c *fakeClient, code int) {
	t.Helper()
	select {
	case info := <-c.closed:
		if info.code != code {
			t.Fatalf("user %d: closed with %d %q, want %d", c.id, info.code, info.reason, code)
		}
	case <-time.After(wait):
		t.Fatalf("user %d: not closed, want %d", c.id, code)
	}
}

func expectDone(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("session did not end")
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never happened: %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// join adds a client and consumes its snapshot frame.
func join(t *testing.T, m *Manager, id int64, role string) (*fakeClient, *Session, decoded) {
	t.Helper()
	c := newClient(id)
	s, err := m.Join(docID, c, role, []domain.Member{{UserID: id, Role: role}})
	if err != nil {
		t.Fatalf("join user %d: %v", id, err)
	}
	return c, s, expect(t, c, TypeSnapshot)
}

func opIn(v, seq int64, op ...ot.Component) Inbound {
	return Inbound{Type: TypeOp, V: v, Seq: seq, Op: ot.Operation(op)}
}

func view(t *testing.T, s *Session) View {
	t.Helper()
	v, ok := s.View()
	if !ok {
		t.Fatal("session is gone")
	}
	return v
}

// ---- Open -----------------------------------------------------------------

func TestOpenFoldsLogOverSnapshot(t *testing.T) {
	store := newStore("ab", 1)
	store.seedOp(2, ot.Operation{ot.Retain(2), ot.Insert("ç")})
	store.seedOp(3, ot.Operation{ot.Delete(1), ot.Retain(2)})
	m := NewManager(store, testLimits())

	_, _, snap := join(t, m, 1, domain.RoleOwner)
	if snap.Content != "bç" || snap.V != 3 || snap.Role != domain.RoleOwner || snap.Title != "Design notes" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.Members) != 1 || len(snap.Presence) != 1 || snap.Presence[0].UserID != 1 {
		t.Fatalf("members/presence = %+v / %+v", snap.Members, snap.Presence)
	}
}

func TestOpenSnapshotsAfterLongReplay(t *testing.T) {
	store := newStore("", 0)
	lim := testLimits()
	for v := int64(1); v <= int64(lim.RingSize)+1; v++ {
		store.seedOp(v, ot.Normalize(ot.Operation{ot.Retain(int(v - 1)), ot.Insert("x")}))
	}
	m := NewManager(store, lim)

	join(t, m, 1, domain.RoleOwner)
	eventually(t, "snapshot after a replay longer than the ring", func() bool {
		snap, ok := store.lastSnap()
		return ok && snap.version == int64(lim.RingSize)+1 && snap.content == "xxxxxxxxx"
	})
}

func TestOpenFailureReachesTheJoiner(t *testing.T) {
	store := newStore("", 0)
	store.doc = nil
	m := NewManager(store, testLimits())

	if _, err := m.Join(docID, newClient(1), domain.RoleOwner, nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("join error = %v, want sql.ErrNoRows", err)
	}
	if m.Lookup(docID) != nil {
		t.Fatal("failed session stayed in the registry")
	}
}

func TestOpenRefusesALogWithAGap(t *testing.T) {
	store := newStore("", 0)
	store.seedOp(2, ot.Operation{ot.Insert("x")})
	m := NewManager(store, testLimits())

	if _, err := m.Join(docID, newClient(1), domain.RoleOwner, nil); err == nil {
		t.Fatal("joined a document whose log skips a version")
	}
}

// ---- Ops ------------------------------------------------------------------

func TestOpIsPersistedBeforeAnyoneHearsOfIt(t *testing.T) {
	store := newStore("", 0)
	store.gate = make(chan struct{})
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)

	s.Deliver(a, opIn(0, 7, ot.Insert("x")))
	quiet(t, a)
	quiet(t, b)
	if store.logLen() != 0 {
		t.Fatal("op in the log before the gate opened")
	}

	store.gate <- struct{}{}
	if ack := expect(t, a, TypeAck); ack.V != 1 || ack.Seq != 7 {
		t.Fatalf("ack = %+v", ack)
	}
	got := expect(t, b, TypeOp)
	if got.V != 1 || got.UserID != 1 || got.Seq != 7 || !ot.Equal(got.Op, ot.Operation{ot.Insert("x")}) {
		t.Fatalf("op frame = %+v", got)
	}
	if store.logLen() != 1 {
		t.Fatalf("log has %d ops, want 1", store.logLen())
	}
	quiet(t, a) // the author gets the ack, not the op
}

func TestConcurrentInsertsLogOrderWins(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)

	// Both typed at position 0 of the empty document.
	s.Deliver(a, opIn(0, 1, ot.Insert("a")))
	s.Deliver(b, opIn(0, 1, ot.Insert("b")))

	if ack := expect(t, a, TypeAck); ack.V != 1 {
		t.Fatalf("a's ack = %+v", ack)
	}
	if got := expect(t, b, TypeOp); got.V != 1 || !ot.Equal(got.Op, ot.Operation{ot.Insert("a")}) {
		t.Fatalf("b sees %+v", got)
	}
	if ack := expect(t, b, TypeAck); ack.V != 2 {
		t.Fatalf("b's ack = %+v", ack)
	}
	// b's insert was transformed past a's: it lands after the logged text.
	if got := expect(t, a, TypeOp); got.V != 2 || !ot.Equal(got.Op, ot.Operation{ot.Retain(1), ot.Insert("b")}) {
		t.Fatalf("a sees %+v", got)
	}
	if v := view(t, s); v.Content != "ab" || v.Version != 2 {
		t.Fatalf("view = %+v", v)
	}
}

func TestViewerOpIsRefusedAndTheSocketStays(t *testing.T) {
	store := newStore("abc", 0)
	m := NewManager(store, testLimits())
	v, s, _ := join(t, m, 3, domain.RoleViewer)

	s.Deliver(v, opIn(0, 1, ot.Retain(3), ot.Insert("x")))
	if e := expect(t, v, TypeError); e.Code != "forbidden" {
		t.Fatalf("error = %+v", e)
	}
	s.Deliver(v, Inbound{Type: "nonsense"})
	if e := expect(t, v, TypeError); e.Code != "bad_frame" {
		t.Fatalf("error = %+v", e)
	}
	if got := view(t, s); got.Content != "abc" || got.Version != 0 {
		t.Fatalf("view = %+v", got)
	}
	select {
	case info := <-v.closed:
		t.Fatalf("viewer closed with %d", info.code)
	default:
	}
}

func TestProtocolViolationsClose4400(t *testing.T) {
	cases := []struct {
		name string
		in   Inbound
		code string // error frame sent before the close, if any
	}{
		{"version ahead of the server", opIn(5, 1, ot.Retain(3)), "bad_version"},
		{"negative version", opIn(-1, 1, ot.Retain(3)), "bad_version"},
		{"min int64 version", opIn(math.MinInt64, 1, ot.Retain(3)), "bad_version"},
		{"component over the limit", opIn(0, 1, ot.Retain(3), ot.Delete(1<<40)), ""},
		{"wrong base length", opIn(0, 1, ot.Retain(2), ot.Insert("x")), ""},
		{"not canonical", opIn(0, 1, ot.Retain(1), ot.Retain(2)), ""},
		{"result over the size limit", opIn(0, 1, ot.Retain(3), ot.Insert(string(make([]rune, 62)))), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore("abc", 0)
			m := NewManager(store, testLimits())
			c, s, _ := join(t, m, 1, domain.RoleOwner)

			s.Deliver(c, tc.in)
			if tc.code != "" {
				if e := expect(t, c, TypeError); e.Code != tc.code {
					t.Fatalf("error = %+v", e)
				}
			}
			expectClose(t, c, CloseProtocol)
			if got := view(t, s); got.Content != "abc" || got.Version != 0 {
				t.Fatalf("view = %+v", got)
			}
		})
	}
}

func TestSecondOpBeforeAckCloses4400(t *testing.T) {
	store := newStore("", 0)
	store.gate = make(chan struct{}, 1)
	m := NewManager(store, testLimits())
	c, s, _ := join(t, m, 1, domain.RoleOwner)

	s.Deliver(c, opIn(0, 1, ot.Insert("a")))
	s.Deliver(c, opIn(0, 2, ot.Insert("b")))
	expectClose(t, c, CloseProtocol)
	store.gate <- struct{}{}

	eventually(t, "first op committed", func() bool { return store.logLen() == 1 })
}

func TestTooFarBehindCloses4409(t *testing.T) {
	store := newStore("", 0)
	lim := testLimits()
	m := NewManager(store, lim)
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)

	for i := 0; i <= lim.RingSize; i++ {
		s.Deliver(a, opIn(int64(i), int64(i), ot.Normalize(ot.Operation{ot.Retain(i), ot.Insert("x")})...))
		expect(t, a, TypeAck)
	}
	// b never caught up: its base is one op older than the ring reaches.
	s.Deliver(b, opIn(0, 1, ot.Insert("y")))
	expectClose(t, b, CloseTooFarBehind)
}

func TestStaleOpAtTheEdgeOfTheRingIsTransformed(t *testing.T) {
	store := newStore("", 0)
	lim := testLimits()
	m := NewManager(store, lim)
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)

	for i := 0; i < lim.RingSize; i++ {
		s.Deliver(a, opIn(int64(i), int64(i), ot.Normalize(ot.Operation{ot.Retain(i), ot.Insert("x")})...))
		expect(t, a, TypeAck)
	}
	s.Deliver(b, opIn(0, 1, ot.Insert("y")))
	eventually(t, "b's op applied", func() bool { return view(t, s).Version == int64(lim.RingSize)+1 })
	if got := view(t, s).Content; got != "xxxxxxxxy" {
		t.Fatalf("content = %q", got)
	}
}

func TestLateJoinerDoesNotGetAnOpItsSnapshotHolds(t *testing.T) {
	store := newStore("", 0)
	store.gate = make(chan struct{})
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	b, _, snap := join(t, m, 2, domain.RoleEditor)
	if snap.Content != "x" || snap.V != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}

	store.gate <- struct{}{}
	expect(t, a, TypeAck)
	quiet(t, b)
}

// ---- Writer ---------------------------------------------------------------

func TestWriterBatchesWhatQueuedWhileItWasBusy(t *testing.T) {
	store := newStore("", 0)
	store.gate = make(chan struct{})
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)
	c, _, _ := join(t, m, 3, domain.RoleEditor)

	s.Deliver(a, opIn(0, 1, ot.Insert("a")))
	s.Deliver(b, opIn(0, 1, ot.Insert("b")))
	s.Deliver(c, opIn(0, 1, ot.Insert("c")))
	eventually(t, "all three applied in memory", func() bool { return view(t, s).Version == 3 })

	store.gate <- struct{}{}
	store.gate <- struct{}{}
	expect(t, c, TypeOp)
	eventually(t, "both batches written", func() bool { return store.logLen() == 3 })

	store.mu.Lock()
	batches := append([]int(nil), store.batches...)
	store.mu.Unlock()
	if len(batches) != 2 || batches[0] != 1 || batches[1] != 2 {
		t.Fatalf("batches = %v, want [1 2]", batches)
	}
}

func TestBackpressureStopsReadingSockets(t *testing.T) {
	store := newStore("", 0)
	store.gate = make(chan struct{})
	lim := testLimits()
	lim.MaxPending = 2
	m := NewManager(store, lim)
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)
	c, _, _ := join(t, m, 3, domain.RoleEditor)

	s.Deliver(a, opIn(0, 1, ot.Insert("a")))
	s.Deliver(b, opIn(0, 1, ot.Insert("b")))

	delivered := make(chan struct{})
	go func() {
		s.Deliver(c, opIn(0, 1, ot.Insert("c")))
		close(delivered)
	}()
	select {
	case <-delivered:
		t.Fatal("a third op was read with two uncommitted")
	case <-time.After(60 * time.Millisecond):
	}

	store.gate <- struct{}{}
	select {
	case <-delivered:
	case <-time.After(wait):
		t.Fatal("reading did not resume after the commit")
	}
	store.gate <- struct{}{}
	store.gate <- struct{}{}
	eventually(t, "all three written", func() bool { return store.logLen() == 3 })
}

func TestInsertFailureClosesEveryoneWith1011(t *testing.T) {
	store := newStore("", 0)
	store.appendErr = errors.New("connection refused")
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)

	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	expectClose(t, a, CloseServerError)
	expectClose(t, b, CloseServerError)
	expectDone(t, s)
	if m.Lookup(docID) != nil {
		t.Fatal("dead session stayed in the registry")
	}
	quiet(t, a) // no ack for an op the database does not have
}

func TestVersionConflictSendsEveryoneToReload(t *testing.T) {
	store := newStore("", 0)
	store.appendErr = domain.ErrVersionTaken
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	expectClose(t, a, CloseTooFarBehind)
	expectDone(t, s)
}

// ---- Snapshots and lifetime -----------------------------------------------

func TestSnapshotEveryNOps(t *testing.T) {
	store := newStore("", 0)
	lim := testLimits()
	lim.SnapshotEveryOps = 3
	m := NewManager(store, lim)
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	for i := 0; i < 3; i++ {
		if _, ok := store.lastSnap(); ok {
			t.Fatalf("snapshot after %d ops", i)
		}
		s.Deliver(a, opIn(int64(i), int64(i), ot.Normalize(ot.Operation{ot.Retain(i), ot.Insert("x")})...))
		expect(t, a, TypeAck)
	}
	eventually(t, "snapshot at version 3", func() bool {
		snap, ok := store.lastSnap()
		return ok && snap.version == 3 && snap.content == "xxx"
	})
}

func TestSnapshotOnInterval(t *testing.T) {
	store := newStore("", 0)
	lim := testLimits()
	lim.SnapshotEvery = 20 * time.Millisecond
	m := NewManager(store, lim)
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	expect(t, a, TypeAck)
	eventually(t, "interval snapshot", func() bool {
		snap, ok := store.lastSnap()
		return ok && snap.version == 1
	})
	time.Sleep(60 * time.Millisecond)
	store.mu.Lock()
	n := len(store.snaps)
	store.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d snapshots of an unchanged document, want 1", n)
	}
}

func TestLastLeaveSnapshotsAndKeepsTheSessionWarm(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	expect(t, a, TypeAck)
	s.Leave(a)
	eventually(t, "snapshot on last leave", func() bool {
		snap, ok := store.lastSnap()
		return ok && snap.version == 1 && snap.content == "x"
	})

	_, again, snap := join(t, m, 1, domain.RoleOwner)
	if again != s {
		t.Fatal("a reconnect inside the idle window opened a new session")
	}
	if snap.Content != "x" || snap.V != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestIdleSessionIsDroppedAndReopensFromTheDatabase(t *testing.T) {
	store := newStore("", 0)
	store.snapErr = errors.New("snapshot refused") // the log alone must be enough
	lim := testLimits()
	lim.Idle = 20 * time.Millisecond
	m := NewManager(store, lim)
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	expect(t, a, TypeAck)
	s.Leave(a)
	expectDone(t, s)
	if m.Lookup(docID) != nil {
		t.Fatal("idle session stayed in the registry")
	}

	_, again, snap := join(t, m, 1, domain.RoleOwner)
	if again == s {
		t.Fatal("joined the ended session")
	}
	if snap.Content != "x" || snap.V != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestLeaveWithAnOpInFlightStillWritesIt(t *testing.T) {
	store := newStore("", 0)
	store.gate = make(chan struct{})
	lim := testLimits()
	lim.Idle = 20 * time.Millisecond
	m := NewManager(store, lim)
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)

	s.Deliver(a, opIn(0, 1, ot.Insert("a")))
	s.Deliver(b, opIn(0, 1, ot.Insert("b")))
	s.Leave(a)
	s.Leave(b)
	store.gate <- struct{}{}
	store.gate <- struct{}{}
	expectDone(t, s)

	if store.logLen() != 2 {
		t.Fatalf("log has %d ops, want 2", store.logLen())
	}
	if snap, ok := store.lastSnap(); !ok || snap.version != 2 || snap.content != "ab" {
		t.Fatalf("final snapshot = %+v (%v)", snap, ok)
	}
}

func TestShutdownWritesSnapshotsAndCloses1001(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	expect(t, a, TypeAck)

	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	expectClose(t, a, CloseGoingAway)
	if snap, ok := store.lastSnap(); !ok || snap.version != 1 || snap.content != "x" {
		t.Fatalf("snapshot = %+v (%v)", snap, ok)
	}
	if _, err := m.Join(docID, newClient(2), domain.RoleEditor, nil); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("join after shutdown = %v", err)
	}
}

func TestShutdownGivesUpWhenTheContextEnds(t *testing.T) {
	store := newStore("", 0)
	store.gate = make(chan struct{})
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	s.Deliver(a, opIn(0, 1, ot.Insert("x")))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown = %v, want deadline exceeded", err)
	}
	store.gate <- struct{}{}
	expectDone(t, s)
}

// ---- Membership, title, delete --------------------------------------------

func TestDowngradeToViewerKeepsTheSocketAndRefusesOps(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 2, domain.RoleEditor)

	s.MemberChanged(2, domain.RoleViewer)
	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	if e := expect(t, a, TypeError); e.Code != "forbidden" {
		t.Fatalf("error = %+v", e)
	}

	s.MemberChanged(2, domain.RoleEditor)
	s.Deliver(a, opIn(0, 2, ot.Insert("x")))
	expect(t, a, TypeAck)
}

func TestRemovedMemberIsClosedWith4003(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b1, _, _ := join(t, m, 2, domain.RoleEditor)
	b2, _, _ := join(t, m, 2, domain.RoleEditor)

	// Drain a's presence frames from the joins.
	quiet(t, a)

	s.MemberChanged(2, "")
	expectClose(t, b1, CloseRevoked)
	expectClose(t, b2, CloseRevoked)
	p := nextAny(t, a)
	if p.Type != TypePresence || len(p.Users) != 1 || p.Users[0].UserID != 1 {
		t.Fatalf("presence after removal = %+v", p)
	}
}

func TestPresenceListsEachUserOnce(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b1, _, _ := join(t, m, 2, domain.RoleEditor)
	if p := nextAny(t, a); p.Type != TypePresence || len(p.Users) != 2 {
		t.Fatalf("presence after b joined = %+v", p)
	}
	_, _, snap := join(t, m, 2, domain.RoleEditor)
	if len(snap.Presence) != 2 {
		t.Fatalf("two sockets of one user listed separately: %+v", snap.Presence)
	}
	nextAny(t, a)

	s.Leave(b1)
	if p := nextAny(t, a); p.Type != TypePresence || len(p.Users) != 2 {
		t.Fatalf("presence after one of b's sockets left = %+v", p)
	}
}

func TestTitleChangeIsBroadcast(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)

	s.TitleChanged("Renamed")
	if f := expect(t, a, TypeTitle); f.Title != "Renamed" {
		t.Fatalf("title frame = %+v", f)
	}
	if v := view(t, s); v.Title != "Renamed" {
		t.Fatalf("view = %+v", v)
	}
}

func TestDeletedDocumentCloses4004AndWritesNothing(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	s.Deliver(a, opIn(0, 1, ot.Insert("x")))
	expect(t, a, TypeAck)

	s.Deleted()
	expectClose(t, a, CloseDeleted)
	expectDone(t, s)
	if _, ok := store.lastSnap(); ok {
		t.Fatal("snapshot written for a deleted document")
	}
}

// ---- Cursors and slow consumers -------------------------------------------

func TestCursorIsFannedOutToTheOthersOnly(t *testing.T) {
	store := newStore("abc", 4)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleViewer)

	s.Deliver(b, Inbound{Type: TypeCursor, Pos: 1, Sel: 3})
	got := expect(t, a, TypeCursor)
	if got.UserID != 2 || got.Pos != 1 || got.Sel != 3 || got.V != 4 {
		t.Fatalf("cursor frame = %+v", got)
	}
	quiet(t, b)

	s.Deliver(b, Inbound{Type: TypeCursor, Pos: 2, Sel: 4})
	if e := expect(t, b, TypeError); e.Code != "bad_frame" {
		t.Fatalf("error = %+v", e)
	}
	quiet(t, a)
}

func TestCursorFloodIsDroppedSilently(t *testing.T) {
	store := newStore("abc", 0)
	m := NewManager(store, testLimits())
	a, s, _ := join(t, m, 1, domain.RoleOwner)
	b, _, _ := join(t, m, 2, domain.RoleEditor)

	const sent = 200
	for i := 0; i < sent; i++ {
		s.Deliver(b, Inbound{Type: TypeCursor, Pos: 0, Sel: 0})
	}
	view(t, s) // the session has processed every frame above
	got := 0
	for len(a.frames) > 0 {
		if nextAny(t, a).Type == TypeCursor {
			got++
		}
	}
	if got < cursorsPerSecond || got >= sent/2 {
		t.Fatalf("%d of %d cursor frames passed, want about %d", got, sent, cursorsPerSecond)
	}
	quiet(t, b)
}

func TestSlowConsumerIsClosed(t *testing.T) {
	store := newStore("", 0)
	m := NewManager(store, testLimits())
	slow := newClientBuf(1, 1) // the snapshot frame fills it
	s, err := m.Join(docID, slow, domain.RoleOwner, nil)
	if err != nil {
		t.Fatal(err)
	}

	b, _, _ := join(t, m, 2, domain.RoleEditor) // presence to slow does not fit
	expectClose(t, slow, CloseTooFarBehind)
	s.Deliver(b, opIn(0, 1, ot.Insert("x")))
	expect(t, b, TypeAck)
}

// ---- Property -------------------------------------------------------------

// Random edits against current and stale versions: whatever the session
// holds in memory must be exactly the stored snapshot with the log folded
// over it, with no version skipped.
func TestMemoryEqualsFoldedLog(t *testing.T) {
	iterations := 2000
	if testing.Short() {
		iterations = 200
	}
	rng := rand.New(rand.NewSource(1))
	alphabet := []rune("abçğ日本😀\n")

	store := newStore("başlangıç", 0)
	lim := testLimits()
	lim.MaxCodepoints = 1 << 20
	lim.SnapshotEveryOps = 50
	m := NewManager(store, lim)
	clients := make([]*fakeClient, 3)
	var s *Session
	for i := range clients {
		// Room for every op frame of the run: nobody reads the other two
		// clients while one is waiting for its ack.
		clients[i] = newClientBuf(int64(i+1), 2*iterations)
		var err error
		if s, err = m.Join(docID, clients[i], domain.RoleEditor, nil); err != nil {
			t.Fatal(err)
		}
		expect(t, clients[i], TypeSnapshot)
	}

	history := map[int64]string{0: "başlangıç"}
	version := int64(0)
	for i := 0; i < iterations; i++ {
		base := version - int64(rng.Intn(lim.RingSize+1))
		if base < 0 {
			base = 0
		}
		doc := []rune(history[base])
		pos := rng.Intn(len(doc) + 1)
		op := ot.Operation{ot.Retain(pos)}
		rest := len(doc) - pos
		if rest > 0 && rng.Intn(3) == 0 {
			n := 1 + rng.Intn(min(rest, 3))
			op = append(op, ot.Delete(n))
			rest -= n
		} else {
			text := make([]rune, 1+rng.Intn(3))
			for j := range text {
				text[j] = alphabet[rng.Intn(len(alphabet))]
			}
			op = append(op, ot.Insert(string(text)))
		}
		op = ot.Normalize(append(op, ot.Retain(rest)))

		c := clients[rng.Intn(len(clients))]
		s.Deliver(c, Inbound{Type: TypeOp, V: base, Seq: int64(i), Op: op})
		for {
			if d := next(t, c); d.Type == TypeAck {
				version = d.V
				break
			}
		}
		history[version] = view(t, s).Content
		delete(history, version-int64(lim.RingSize)-1)
	}

	if version != int64(iterations) {
		t.Fatalf("version = %d after %d ops", version, iterations)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.ops) != iterations {
		t.Fatalf("log has %d ops, want %d", len(store.ops), iterations)
	}
	folded := "başlangıç"
	for i, row := range store.ops {
		if row.Version != int64(i+1) {
			t.Fatalf("log row %d has version %d", i, row.Version)
		}
		var op ot.Operation
		if err := json.Unmarshal(row.Op, &op); err != nil {
			t.Fatalf("log row %d: %v", i, err)
		}
		var err error
		if folded, err = ot.Apply(folded, op); err != nil {
			t.Fatalf("log row %d does not apply: %v", i, err)
		}
	}
	if folded != history[version] {
		t.Fatalf("memory and folded log differ:\n memory %q\n log    %q", history[version], folded)
	}
	// Every snapshot written on the way is the fold at its version.
	for _, snap := range store.snaps {
		if snap.version > version {
			t.Fatalf("snapshot at %d is ahead of the log (%d)", snap.version, version)
		}
	}
}

func TestRingAfterRefusesVersionsOutsideTheLog(t *testing.T) {
	r := newRing(4)
	for v := int64(1); v <= 5; v++ {
		r.push(ringEntry{version: v})
	}
	for _, v := range []int64{math.MinInt64, -1, 6} {
		if got, ok := r.after(v, 5); ok || got != nil {
			t.Errorf("after(%d, 5) = %v, %v; want nil, false", v, got, ok)
		}
	}
	if got, ok := r.after(0, 5); ok || got != nil {
		t.Errorf("after(0, 5) = %v, %v; want nil, false (older than the ring)", got, ok)
	}
	if got, ok := r.after(2, 5); !ok || len(got) != 3 || got[0].version != 3 {
		t.Errorf("after(2, 5) = %v, %v; want versions 3..5", got, ok)
	}
}
