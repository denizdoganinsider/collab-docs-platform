package loadbalancer

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestConsistentHashIsDeterministic(t *testing.T) {
	pool := newTestPool(t, "http://a:1", "http://b:2", "http://c:3")
	s := NewConsistentHash(pool, DocumentKey)

	first, err := s.Pick(httptest.NewRequest("GET", "/ws?doc=1", nil))
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}

	for range 50 {
		again, err := s.Pick(httptest.NewRequest("GET", "/ws?doc=1", nil))
		if err != nil {
			t.Fatalf("Pick returned error: %v", err)
		}
		if again != first {
			t.Fatalf("same document resolved to %s then %s", first, again)
		}
	}
}

// The property that makes stickiness work without shared state: the same
// document on a freshly built ring (a gateway restart, a second gateway)
// lands on the same instance, because nothing about the mapping lives in
// memory.
func TestConsistentHashSurvivesRebuild(t *testing.T) {
	first := NewConsistentHash(newTestPool(t, "http://a:1", "http://b:2"), DocumentKey)
	second := NewConsistentHash(newTestPool(t, "http://a:1", "http://b:2"), DocumentKey)

	for i := range 200 {
		key := fmt.Sprintf("%d", i)
		if first.lookup(key).String() != second.lookup(key).String() {
			t.Fatalf("document %q moved between two identical rings", key)
		}
	}
}

// Virtual nodes are what keep the split even. Without them a two-instance
// ring is two points on a circle, and the arc between them is whatever the
// hash function happened to produce.
func TestConsistentHashSpreadsDocumentsEvenly(t *testing.T) {
	pool := newTestPool(t, "http://a:1", "http://b:2", "http://c:3")
	s := NewConsistentHash(pool, DocumentKey)

	const documents = 30000
	counts := make(map[string]int)
	for i := range documents {
		counts[s.lookup(fmt.Sprintf("%d", i)).String()]++
	}

	// Perfect is 10000 each; allow ±20%.
	for backend, count := range counts {
		if count < 8000 || count > 12000 {
			t.Errorf("%s got %d of %d documents; expected roughly a third", backend, count, documents)
		}
	}
}

// The reason to bother with a ring rather than hash % N: adding a fourth
// instance moves only the documents that now belong to it (about a quarter),
// not the three quarters that hash % N would reshuffle.
func TestConsistentHashMovesFewKeysWhenScalingOut(t *testing.T) {
	three := NewConsistentHash(newTestPool(t, "http://a:1", "http://b:2", "http://c:3"), DocumentKey)
	four := NewConsistentHash(newTestPool(t, "http://a:1", "http://b:2", "http://c:3", "http://d:4"), DocumentKey)

	const documents = 30000
	moved := 0
	for i := range documents {
		key := fmt.Sprintf("%d", i)
		if three.lookup(key).String() != four.lookup(key).String() {
			moved++
		}
	}

	// Ideal is 25%. hash % N would move ~75%; anything under 35% proves the ring.
	if moved > documents*35/100 {
		t.Errorf("%d of %d documents moved when adding an instance; want about a quarter", moved, documents)
	}
	if moved < documents*15/100 {
		t.Errorf("only %d of %d documents moved; the new instance is not getting its share", moved, documents)
	}
}

// Fail closed, not reroute: see the ConsistentHash doc comment.
func TestConsistentHashFailsClosedWhenInstanceDown(t *testing.T) {
	pool := newTestPool(t, "http://a:1", "http://b:2")
	s := NewConsistentHash(pool, DocumentKey)

	victim := s.lookup("1")
	victim.setHealthy(false)

	_, err := s.Pick(httptest.NewRequest("GET", "/ws?doc=1", nil))

	var down *ErrBackendDown
	if !errors.As(err, &down) {
		t.Fatalf("Pick error = %v, want ErrBackendDown", err)
	}
	if down.Backend != victim || down.Key != "1" {
		t.Errorf("ErrBackendDown = %+v, want backend %s for key 1", down, victim)
	}

	// Documents that hash elsewhere are unaffected.
	for i := range 200 {
		key := fmt.Sprintf("%d", i)
		if s.lookup(key) == victim {
			continue
		}
		if _, err := s.Pick(httptest.NewRequest("GET", "/ws?doc="+key, nil)); err != nil {
			t.Fatalf("document %q on a healthy instance failed: %v", key, err)
		}
	}
}

// The difference from the chat project: the socket and every HTTP request
// about the same document must agree on the instance, because the live
// session answers reads and receives membership changes.
func TestDocumentKeyIsTheSameForSocketAndHTTP(t *testing.T) {
	cases := map[string]string{
		"/ws?doc=42":                      "42",
		"/ws?doc=42&ticket=abc":           "42",
		"/documents/42":                   "42",
		"/documents/42/ops?from=3":        "42",
		"/documents/42/members":           "42",
		"/documents/42/members/7":         "42",
		"/documents/042":                  "42", // doc-service parses the id as an integer
		"/ws?doc=042":                     "42",
		"/documents/not-a-number":         "not-a-number",
		"/ws?doc=not-a-number":            "not-a-number",
		"/documents":                      "",
		"/ws":                             "",
		"/documents/42extra/members":      "42extra",
		"/documents/-1":                   "-1",
		"/documents/99999999999999999999": "99999999999999999999", // overflow: raw string, still deterministic
		"/documents/42?doc=7":             "42",                   // the path wins over a stray query param
		"/admin/documents":                "",
	}

	for target, want := range cases {
		if got := DocumentKey(httptest.NewRequest("GET", target, nil)); got != want {
			t.Errorf("DocumentKey(%s) = %q, want %q", target, got, want)
		}
	}
}
