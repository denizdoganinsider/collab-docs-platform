// Package session holds the live state of open documents: one Session per
// document, owned by a single goroutine and reached only through channels.
// There is no mutex around document state; ordering is that goroutine's
// serial execution.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"
	"unicode/utf8"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/ot"
)

// Limits are the README's session limits. MaxPending bounds the ops applied
// in memory but not yet committed; at the bound the session stops reading
// from sockets instead of dropping edits.
type Limits struct {
	MaxCodepoints    int
	RingSize         int
	SnapshotEveryOps int
	SnapshotEvery    time.Duration
	Idle             time.Duration
	MaxPending       int
}

func DefaultLimits() Limits {
	return Limits{
		MaxCodepoints:    1 << 20,
		RingSize:         1000,
		SnapshotEveryOps: 100,
		SnapshotEvery:    30 * time.Second,
		Idle:             60 * time.Second,
		MaxPending:       256,
	}
}

const (
	replayPage = 500

	// Cursor frames beyond this rate are dropped without a reply.
	cursorsPerSecond = 20
)

// Client is a connected socket as the session sees it. Implementations must
// be comparable (pointers).
type Client interface {
	UserID() int64
	// Send queues a frame without blocking. False means the client's buffer
	// is full; the session then closes it.
	Send(frame []byte) bool
	// Close closes the socket with a WebSocket close code. It must be safe
	// to call more than once.
	Close(code int, reason string)
}

// View is the document as the session currently holds it.
type View struct {
	Title   string
	Content string
	Version int64
}

// errGone: the session ended normally before the request reached it. The
// manager opens a new one.
var errGone = errors.New("session: closed")

type clientState struct {
	role     string
	joinedAt int64 // version the snapshot frame carried; older ops are in it
	awaiting bool  // an op is applied but not yet acked

	cursorTokens float64
	cursorAt     time.Time
}

type joinReq struct {
	client  Client
	role    string
	members []domain.Member
	reply   chan error
}

type frame struct {
	client Client
	in     Inbound
}

type eventKind int

const (
	eventMember eventKind = iota
	eventTitle
	eventDeleted
)

type event struct {
	kind   eventKind
	userID int64
	role   string // "" means removed
	title  string
}

type Session struct {
	docID  int64
	store  Store
	lim    Limits
	onExit func(*Session)

	joins   chan joinReq
	leaves  chan Client
	frames  chan frame
	events  chan event
	views   chan chan View
	stop    chan chan struct{}
	jobs    chan job
	results chan result
	done    chan struct{}

	// loadErr is written before done is closed and read only after.
	loadErr error

	// Everything below is owned by run().
	title        string
	content      string
	length       int // code points in content
	version      int64
	ring         *ring
	clients      map[Client]*clientState
	queue        []pending // applied, not yet handed to the writer
	inFlight     int       // handed to the writer, not yet committed
	busy         bool
	dirty        bool // content is ahead of the stored snapshot
	pendingSince int  // ops since the stored snapshot
	snapDue      bool
	idle         *time.Timer
	idleC        <-chan time.Time
}

func newSession(docID int64, store Store, lim Limits, onExit func(*Session)) *Session {
	s := &Session{
		docID:   docID,
		store:   store,
		lim:     lim,
		onExit:  onExit,
		joins:   make(chan joinReq),
		leaves:  make(chan Client),
		frames:  make(chan frame),
		events:  make(chan event),
		views:   make(chan chan View),
		stop:    make(chan chan struct{}),
		jobs:    make(chan job, 1),
		results: make(chan result, 1),
		done:    make(chan struct{}),
		ring:    newRing(lim.RingSize),
		clients: make(map[Client]*clientState),
	}
	go s.run()
	return s
}

// ---- Entry points (any goroutine) ----------------------------------------

func (s *Session) join(c Client, role string, members []domain.Member) error {
	req := joinReq{client: c, role: role, members: members, reply: make(chan error, 1)}
	select {
	case s.joins <- req:
		return <-req.reply
	case <-s.done:
		if s.loadErr != nil {
			return s.loadErr
		}
		return errGone
	}
}

// Leave removes a client. Called by the read pump when the socket ends; a
// client the session already closed is ignored.
func (s *Session) Leave(c Client) {
	select {
	case s.leaves <- c:
	case <-s.done:
	}
}

// Deliver hands a decoded client frame to the session. It blocks while the
// session is not reading: that is the backpressure on a socket whose edits
// the database has not caught up with. False means the session is gone.
func (s *Session) Deliver(c Client, in Inbound) bool {
	select {
	case s.frames <- frame{client: c, in: in}:
		return true
	case <-s.done:
		return false
	}
}

// MemberChanged tells the session a user's role changed; role "" means the
// user was removed from the document.
func (s *Session) MemberChanged(userID int64, role string) {
	s.event(event{kind: eventMember, userID: userID, role: role})
}

func (s *Session) TitleChanged(title string) {
	s.event(event{kind: eventTitle, title: title})
}

// Deleted closes every socket with 4004 and drops the session without
// writing anything: the rows are gone.
func (s *Session) Deleted() {
	s.event(event{kind: eventDeleted})
}

func (s *Session) event(ev event) {
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

// View returns the live document; false when the session is gone.
func (s *Session) View() (View, bool) {
	reply := make(chan View, 1)
	select {
	case s.views <- reply:
		return <-reply, true
	case <-s.done:
		return View{}, false
	}
}

// Stop persists what is pending, snapshots if dirty, closes every socket
// with 1001 and returns when the session has ended.
func (s *Session) Stop() {
	ack := make(chan struct{})
	select {
	case s.stop <- ack:
		<-ack
	case <-s.done:
	}
}

// Done is closed when the session has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// ---- The session goroutine -----------------------------------------------

func (s *Session) run() {
	go writer(s.docID, s.store, s.jobs, s.results)
	defer func() {
		close(s.jobs)
		if s.idle != nil {
			s.idle.Stop()
		}
		// Leave the registry before done is closed: a join that was waiting
		// on this session must find the map free for its replacement.
		s.onExit(s)
		close(s.done)
	}()

	if err := s.load(); err != nil {
		slog.Error("session load", "doc_id", s.docID, "error", err)
		s.loadErr = err
		return
	}
	s.armIdle()

	ticker := time.NewTicker(s.lim.SnapshotEvery)
	defer ticker.Stop()

	for {
		// Backpressure: with too many uncommitted ops, do not read sockets.
		frames := s.frames
		if len(s.queue)+s.inFlight >= s.lim.MaxPending {
			frames = nil
		}

		select {
		case req := <-s.joins:
			s.handleJoin(req)
		case c := <-s.leaves:
			s.remove(c)
		case f := <-frames:
			s.handleFrame(f)
		case res := <-s.results:
			if !s.handleResult(res) {
				return
			}
		case ev := <-s.events:
			if !s.handleEvent(ev) {
				return
			}
		case reply := <-s.views:
			reply <- View{Title: s.title, Content: s.content, Version: s.version}
		case <-ticker.C:
			if s.dirty {
				s.snapDue = true
			}
		case <-s.idleC:
			if len(s.clients) == 0 {
				s.flush()
				return
			}
		case ack := <-s.stop:
			if s.flush() {
				s.closeAll(CloseGoingAway, "shutdown")
			}
			close(ack)
			return
		}

		s.dispatch()
	}
}

// load reads the snapshot and folds in every op logged after it.
func (s *Session) load() error {
	doc, err := s.store.Load(s.docID)
	if err != nil {
		return err
	}
	s.title, s.content, s.version = doc.Title, doc.Content, doc.Version

	replayed := 0
	for {
		rows, err := s.store.OpsAfter(s.docID, s.version, replayPage)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Version != s.version+1 {
				return fmt.Errorf("op log of document %d jumps from version %d to %d", s.docID, s.version, row.Version)
			}
			var op ot.Operation
			if err := json.Unmarshal(row.Op, &op); err != nil {
				return fmt.Errorf("op %d of document %d: %w", row.Version, s.docID, err)
			}
			content, err := ot.Apply(s.content, op)
			if err != nil {
				return fmt.Errorf("op %d of document %d: %w", row.Version, s.docID, err)
			}
			s.content, s.version = content, row.Version
			s.ring.push(ringEntry{version: row.Version, op: op})
			replayed++
		}
		if len(rows) < replayPage {
			break
		}
	}

	s.length = utf8.RuneCountInString(s.content)
	s.pendingSince = replayed
	s.dirty = replayed > 0
	// A long replay is paid by every open until someone snapshots.
	s.snapDue = replayed > s.lim.RingSize
	return nil
}

func (s *Session) handleJoin(req joinReq) {
	s.disarmIdle()
	s.clients[req.client] = &clientState{
		role:         req.role,
		joinedAt:     s.version,
		cursorTokens: cursorsPerSecond,
		cursorAt:     time.Now(),
	}
	req.reply <- nil

	s.send(req.client, encode(snapshotFrame{
		Type:     TypeSnapshot,
		DocID:    s.docID,
		Title:    s.title,
		V:        s.version,
		Content:  s.content,
		Role:     req.role,
		Members:  req.members,
		Presence: s.presence(),
	}))
	s.broadcastPresence(req.client)
}

func (s *Session) handleFrame(f frame) {
	st := s.clients[f.client]
	if st == nil {
		return // closed by the session while this frame was on its way
	}
	switch f.in.Type {
	case TypeOp:
		s.handleOp(f.client, st, f.in)
	case TypeCursor:
		s.handleCursor(f.client, st, f.in)
	case TypePing:
	default:
		s.sendError(f.client, "bad_frame", "unknown frame type")
	}
}

func (s *Session) handleOp(c Client, st *clientState, in Inbound) {
	if !domain.CanEdit(st.role) {
		s.sendError(c, "forbidden", "viewers cannot edit")
		return
	}
	if st.awaiting {
		s.evict(c, CloseProtocol, "second op before ack")
		return
	}
	if in.V > s.version {
		s.sendError(c, "bad_version", "base version is ahead of the server")
		s.evict(c, CloseProtocol, "bad_version")
		return
	}
	missed, ok := s.ring.after(in.V, s.version)
	if !ok {
		s.evict(c, CloseTooFarBehind, reasonTooFarBehind)
		return
	}

	op := in.Op
	if err := ot.Validate(op, s.lim.MaxCodepoints); err != nil {
		s.evict(c, CloseProtocol, err.Error())
		return
	}
	// Transform past everything the client had not seen, the logged op as
	// the first argument so the log wins position ties. A wrong base length
	// surfaces here or in Apply.
	for _, e := range missed {
		var err error
		if _, op, err = ot.Transform(e.op, op); err != nil {
			s.evict(c, CloseProtocol, err.Error())
			return
		}
	}
	if ot.TargetLen(op) > s.lim.MaxCodepoints {
		s.sendError(c, "too_large", "document would exceed the size limit")
		s.evict(c, CloseProtocol, "too_large")
		return
	}
	content, err := ot.Apply(s.content, op)
	if err != nil {
		s.evict(c, CloseProtocol, err.Error())
		return
	}

	s.content, s.length = content, ot.TargetLen(op)
	s.version++
	s.ring.push(ringEntry{version: s.version, op: op})
	s.queue = append(s.queue, pending{
		client: c,
		seq:    in.Seq,
		op:     domain.Op{DocID: s.docID, Version: s.version, UserID: c.UserID(), Op: encode(op)},
	})
	st.awaiting = true
	s.dirty = true
	s.pendingSince++
	if s.pendingSince >= s.lim.SnapshotEveryOps {
		s.snapDue = true
	}
}

func (s *Session) handleCursor(c Client, st *clientState, in Inbound) {
	now := time.Now()
	st.cursorTokens += now.Sub(st.cursorAt).Seconds() * cursorsPerSecond
	if st.cursorTokens > cursorsPerSecond {
		st.cursorTokens = cursorsPerSecond
	}
	st.cursorAt = now
	if st.cursorTokens < 1 {
		return
	}
	st.cursorTokens--

	if in.Pos < 0 || in.Sel < in.Pos || in.Sel > s.length {
		s.sendError(c, "bad_frame", "cursor outside the document")
		return
	}
	msg := encode(cursorFrame{Type: TypeCursor, UserID: c.UserID(), Pos: in.Pos, Sel: in.Sel, V: s.version})
	for other := range s.clients {
		if other != c {
			s.send(other, msg)
		}
	}
}

// dispatch hands the writer everything queued, plus a snapshot when one is
// due. Under load the queue grows while the writer is busy, so batching is
// what the commit latency makes it.
func (s *Session) dispatch() {
	if s.busy {
		return
	}
	if !s.dirty {
		s.snapDue = false
	}
	if len(s.queue) == 0 && !s.snapDue {
		return
	}
	j := job{ops: s.queue}
	if s.snapDue {
		j.snap = &snapshot{content: s.content, version: s.version}
		s.snapDue = false
	}
	s.queue = nil
	s.inFlight = len(j.ops)
	s.busy = true
	s.jobs <- j // capacity 1 and the writer is idle: never blocks
}

// handleResult runs when a job has returned. On a commit it acks the author
// and tells everyone else; false means the session cannot continue.
func (s *Session) handleResult(res result) bool {
	s.busy = false
	s.inFlight = 0

	if res.err != nil {
		if errors.Is(res.err, domain.ErrVersionTaken) {
			// Someone else wrote these versions: memory is behind the log.
			slog.Warn("session op log conflict", "doc_id", s.docID, "version", s.version)
			s.closeAll(CloseTooFarBehind, reasonTooFarBehind)
		} else {
			slog.Error("session op insert", "doc_id", s.docID, "error", res.err)
			s.closeAll(CloseServerError, "internal error")
		}
		return false
	}

	for _, p := range res.job.ops {
		if st := s.clients[p.client]; st != nil {
			st.awaiting = false
			s.send(p.client, encode(ackFrame{Type: TypeAck, V: p.op.Version, Seq: p.seq}))
		}
		msg := encode(opFrame{Type: TypeOp, V: p.op.Version, UserID: p.op.UserID, Op: p.op.Op, Seq: p.seq})
		for c, st := range s.clients {
			// A client that joined at or after this version got it in its
			// snapshot frame.
			if c != p.client && st.joinedAt < p.op.Version {
				s.send(c, msg)
			}
		}
	}

	if snap := res.job.snap; snap != nil {
		if res.snapErr != nil {
			// Not fatal: the log has every op. The interval retries.
			slog.Error("session snapshot", "doc_id", s.docID, "error", res.snapErr)
		} else {
			s.pendingSince = int(s.version - snap.version)
			s.dirty = s.pendingSince > 0
		}
	}
	return true
}

func (s *Session) handleEvent(ev event) bool {
	switch ev.kind {
	case eventDeleted:
		s.closeAll(CloseDeleted, "document deleted")
		return false
	case eventTitle:
		s.title = ev.title
		msg := encode(titleFrame{Type: TypeTitle, Title: ev.title})
		for c := range s.clients {
			s.send(c, msg)
		}
	case eventMember:
		removed := false
		for c, st := range s.clients {
			if c.UserID() != ev.userID {
				continue
			}
			if ev.role == "" {
				delete(s.clients, c)
				c.Close(CloseRevoked, "membership revoked")
				removed = true
				continue
			}
			st.role = ev.role
		}
		if removed {
			s.afterLeave()
		}
		s.broadcastPresence(nil)
	}
	return true
}

// flush is the last thing a session does: wait for the writer, write what is
// still queued and, if the content is ahead of the snapshot, snapshot once.
// The session is ending, so blocking here blocks nobody it still serves.
// False means the insert failed and the sockets are already closed.
func (s *Session) flush() bool {
	snapTried := false
	for {
		if s.busy {
			if !s.handleResult(<-s.results) {
				return false
			}
			continue
		}
		if len(s.queue) == 0 && (!s.dirty || snapTried) {
			return true
		}
		s.snapDue = s.dirty && !snapTried
		snapTried = true
		s.dispatch()
	}
}

// ---- Helpers --------------------------------------------------------------

func (s *Session) send(c Client, msg []byte) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	if !c.Send(msg) {
		// A client that cannot keep up is behind and must reload anyway.
		s.evict(c, CloseTooFarBehind, "slow consumer")
	}
}

func (s *Session) sendError(c Client, code, message string) {
	s.send(c, encode(errorFrame{Type: TypeError, Code: code, Message: message}))
}

func (s *Session) evict(c Client, code int, reason string) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	c.Close(code, reason)
	s.remove(c)
}

func (s *Session) remove(c Client) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	delete(s.clients, c)
	s.afterLeave()
	s.broadcastPresence(nil)
}

// afterLeave: with nobody left, snapshot if dirty and keep the session warm
// for a reconnecting tab until the idle timer fires.
func (s *Session) afterLeave() {
	if len(s.clients) > 0 {
		return
	}
	if s.dirty {
		s.snapDue = true
	}
	s.armIdle()
}

func (s *Session) closeAll(code int, reason string) {
	for c := range s.clients {
		delete(s.clients, c)
		c.Close(code, reason)
	}
}

func (s *Session) presence() []presenceUser {
	seen := make(map[int64]bool, len(s.clients))
	users := make([]presenceUser, 0, len(s.clients))
	for c := range s.clients {
		if id := c.UserID(); !seen[id] {
			seen[id] = true
			users = append(users, presenceUser{UserID: id})
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })
	return users
}

// broadcastPresence sends the presence list to everyone but except (who has
// just received it in a snapshot frame).
func (s *Session) broadcastPresence(except Client) {
	if len(s.clients) == 0 {
		return
	}
	msg := encode(presenceFrame{Type: TypePresence, Users: s.presence()})
	for c := range s.clients {
		if c != except {
			s.send(c, msg)
		}
	}
}

func (s *Session) armIdle() {
	s.disarmIdle()
	s.idle = time.NewTimer(s.lim.Idle)
	s.idleC = s.idle.C
}

func (s *Session) disarmIdle() {
	if s.idle != nil {
		s.idle.Stop()
		s.idle, s.idleC = nil, nil
	}
}
