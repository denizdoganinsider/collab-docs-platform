package service

import (
	"testing"
	"time"
)

func TestIssueThenRedeemReturnsTheIdentity(t *testing.T) {
	s := NewTicketService()
	defer s.Close()

	ticket, err := s.Issue(42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	userID, role, ok := s.Redeem(ticket)
	if !ok {
		t.Fatal("Redeem rejected a freshly issued ticket")
	}
	if userID != 42 || role != "user" {
		t.Errorf("Redeem returned %d/%q, want 42/user", userID, role)
	}
}

// The whole point of a ticket over a token: capturing one buys a single
// connection at most, and only for the seconds before it is spent.
func TestRedeemIsSingleUse(t *testing.T) {
	s := NewTicketService()
	defer s.Close()

	ticket, err := s.Issue(1, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	if _, _, ok := s.Redeem(ticket); !ok {
		t.Fatal("first Redeem failed")
	}

	if _, _, ok := s.Redeem(ticket); ok {
		t.Error("second Redeem succeeded; a replayed ticket must be worthless")
	}
}

func TestRedeemRejectsUnknownTicket(t *testing.T) {
	s := NewTicketService()
	defer s.Close()

	if _, _, ok := s.Redeem("not-a-ticket"); ok {
		t.Error("Redeem accepted a ticket that was never issued")
	}
}

func TestRedeemRejectsExpiredTicket(t *testing.T) {
	s := NewTicketService()
	defer s.Close()

	ticket, err := s.Issue(1, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	// Reach in and age it rather than sleeping out the real TTL.
	s.mu.Lock()
	entry := s.tickets[ticket]
	entry.expiresAt = time.Now().Add(-time.Second)
	s.tickets[ticket] = entry
	s.mu.Unlock()

	if _, _, ok := s.Redeem(ticket); ok {
		t.Error("Redeem accepted an expired ticket")
	}
}

func TestIssueReturnsDistinctTickets(t *testing.T) {
	s := NewTicketService()
	defer s.Close()

	seen := make(map[string]bool)
	for range 100 {
		ticket, err := s.Issue(1, "user")
		if err != nil {
			t.Fatalf("Issue returned error: %v", err)
		}
		if seen[ticket] {
			t.Fatal("Issue produced a duplicate ticket")
		}
		seen[ticket] = true
	}
}

// Tickets nobody redeems are never read again, so the reaper is the only thing
// keeping the map from growing for the life of the process.
func TestReapExpiredRemovesStaleTickets(t *testing.T) {
	s := NewTicketService()
	defer s.Close()

	ticket, err := s.Issue(1, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	s.mu.Lock()
	entry := s.tickets[ticket]
	entry.expiresAt = time.Now().Add(-time.Second)
	s.tickets[ticket] = entry
	s.mu.Unlock()

	s.reapOnce(time.Now())

	s.mu.Lock()
	remaining := len(s.tickets)
	s.mu.Unlock()

	if remaining != 0 {
		t.Errorf("%d expired tickets left in the map, want 0", remaining)
	}
}
