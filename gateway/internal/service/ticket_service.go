package service

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	// Long enough to survive the round trip from POST /ws-ticket to the
	// WebSocket handshake, short enough that a ticket found in a log or in
	// browser history is already dead.
	TicketTTL = 30 * time.Second

	ticketReapInterval = 30 * time.Second
)

type ticketEntry struct {
	userID    int64
	role      string
	expiresAt time.Time
}

// TicketService exists because browsers cannot set headers on a WebSocket
// handshake, which would put the long-lived JWT in the /ws URL where proxies
// and access logs can capture it. A client exchanges its token for a one-shot
// ticket and spends that instead; the gateway redeems it and forwards the
// identity as X-User-ID on the outbound upgrade request, where headers work.
//
// The store is in-memory on purpose. The gateway is a single process, and stays
// one in month 3 where it becomes the load balancer rather than being placed
// behind one. If it is ever replicated, this map is the thing to move to Redis.
type TicketService struct {
	mu      sync.Mutex
	tickets map[string]ticketEntry
	stop    chan struct{}
}

func NewTicketService() *TicketService {
	s := &TicketService{
		tickets: make(map[string]ticketEntry),
		stop:    make(chan struct{}),
	}

	go s.reapExpired()

	return s
}

func (s *TicketService) Close() {
	close(s.stop)
}

func (s *TicketService) Issue(userID int64, role string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	ticket := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.tickets[ticket] = ticketEntry{
		userID:    userID,
		role:      role,
		expiresAt: time.Now().Add(TicketTTL),
	}

	return ticket, nil
}

// Redeem deletes on read: a ticket buys exactly one connection, so replaying a
// captured one buys nothing.
func (s *TicketService) Redeem(ticket string) (int64, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.tickets[ticket]
	if !ok {
		return 0, "", false
	}

	delete(s.tickets, ticket)

	if time.Now().After(entry.expiresAt) {
		return 0, "", false
	}

	return entry.userID, entry.role, true
}

// Unredeemed tickets are never read again but would sit in the map forever, so
// they get the same treatment the hub gives empty rooms: a periodic sweep.
func (s *TicketService) reapExpired() {
	ticker := time.NewTicker(ticketReapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.reapOnce(time.Now())
		}
	}
}

func (s *TicketService) reapOnce(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for ticket, entry := range s.tickets {
		if now.After(entry.expiresAt) {
			delete(s.tickets, ticket)
		}
	}
}
