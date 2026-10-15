package loadbalancer

import "testing"

func newTestPool(t *testing.T, urls ...string) *Pool {
	t.Helper()

	pool, err := NewPool(urls)
	if err != nil {
		t.Fatalf("NewPool returned error: %v", err)
	}

	return pool
}

func TestRoundRobinRotatesThroughEveryBackend(t *testing.T) {
	pool := newTestPool(t, "http://a:1", "http://b:2", "http://c:3")
	s := NewRoundRobin(pool)

	want := []string{"a:1", "b:2", "c:3", "a:1", "b:2", "c:3"}
	for i, expected := range want {
		b, err := s.Pick(nil)
		if err != nil {
			t.Fatalf("pick %d returned error: %v", i, err)
		}
		if b.String() != expected {
			t.Errorf("pick %d = %s, want %s", i, b, expected)
		}
	}
}

// Skipping a dead instance must not hand its turn to the next one twice.
func TestRoundRobinSkipsUnhealthyEvenly(t *testing.T) {
	pool := newTestPool(t, "http://a:1", "http://b:2", "http://c:3")
	pool.Backends()[1].setHealthy(false)

	s := NewRoundRobin(pool)

	counts := make(map[string]int)
	for range 100 {
		b, err := s.Pick(nil)
		if err != nil {
			t.Fatalf("Pick returned error: %v", err)
		}
		counts[b.String()]++
	}

	if counts["b:2"] != 0 {
		t.Errorf("unhealthy backend was picked %d times", counts["b:2"])
	}
	if counts["a:1"] != 50 || counts["c:3"] != 50 {
		t.Errorf("uneven split across healthy backends: %v", counts)
	}
}

func TestRoundRobinFailsWhenAllDown(t *testing.T) {
	pool := newTestPool(t, "http://a:1")
	pool.Backends()[0].setHealthy(false)

	if _, err := NewRoundRobin(pool).Pick(nil); err != ErrNoHealthyBackend {
		t.Errorf("Pick error = %v, want ErrNoHealthyBackend", err)
	}
}

func TestNewPoolRejectsBadInput(t *testing.T) {
	cases := map[string][]string{
		"empty":       {},
		"no host":     {"not-a-url"},
		"no scheme":   {"doc-service:9001"},
		"duplicate":   {"http://a:1", "http://a:1"},
		"unparseable": {"http://a:1", "://"},
	}

	for name, urls := range cases {
		if _, err := NewPool(urls); err == nil {
			t.Errorf("%s: NewPool accepted %v", name, urls)
		}
	}
}
