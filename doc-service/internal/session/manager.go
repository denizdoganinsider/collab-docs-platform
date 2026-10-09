package session

import (
	"context"
	"errors"
	"sync"

	"collab-docs-platform/doc-service/internal/domain"
)

// ErrShuttingDown: the process is stopping and opens no more sessions.
var ErrShuttingDown = errors.New("session: shutting down")

// Manager is the registry of live sessions on this instance, one per open
// document. The mutex guards the map only; document state stays inside each
// session's goroutine.
type Manager struct {
	store Store
	lim   Limits

	mu       sync.Mutex
	sessions map[int64]*Session
	closed   bool
}

func NewManager(store Store, lim Limits) *Manager {
	return &Manager{store: store, lim: lim, sessions: make(map[int64]*Session)}
}

// Join adds the client to the document's session, opening it (snapshot plus
// log replay) if this is the first socket. members goes into the snapshot
// frame as given. A load failure is returned as the store reported it.
func (m *Manager) Join(docID int64, c Client, role string, members []domain.Member) (*Session, error) {
	for {
		s, err := m.open(docID)
		if err != nil {
			return nil, err
		}
		err = s.join(c, role, members)
		if err == nil {
			return s, nil
		}
		if !errors.Is(err, errGone) {
			return nil, err
		}
		// The session idled out between the lookup and the join. It has
		// left the map; open a fresh one.
	}
}

// Lookup returns the live session of a document, or nil when none is open
// on this instance.
func (m *Manager) Lookup(docID int64) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[docID]
}

// View returns the document as the live session holds it; false when no
// session is open on this instance (the caller reads the database).
func (m *Manager) View(docID int64) (View, bool) {
	s := m.Lookup(docID)
	if s == nil {
		return View{}, false
	}
	return s.View()
}

func (m *Manager) open(docID int64) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrShuttingDown
	}
	if s, ok := m.sessions[docID]; ok {
		return s, nil
	}
	s := newSession(docID, m.store, m.lim, m.forget)
	m.sessions[docID] = s
	return s, nil
}

func (m *Manager) forget(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.docID] == s {
		delete(m.sessions, s.docID)
	}
}

// Shutdown refuses new sessions, then stops every live one: pending ops are
// written, dirty documents snapshotted, sockets closed with 1001. It returns
// when all have ended or ctx is done.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	live := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		live = append(live, s)
	}
	m.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, s := range live {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.Stop()
			}()
		}
		wg.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
