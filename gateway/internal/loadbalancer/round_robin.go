package loadbalancer

import (
	"net/http"
	"sync/atomic"
)

// RoundRobin hands consecutive requests to consecutive healthy instances. It
// is the right strategy exactly when any instance can answer - here, the
// user-scoped document list and create, which are database operations that
// touch no live session.
type RoundRobin struct {
	pool *Pool
	next atomic.Uint64
}

func NewRoundRobin(pool *Pool) *RoundRobin {
	return &RoundRobin{pool: pool}
}

func (s *RoundRobin) Name() string { return "round-robin" }

func (s *RoundRobin) Pick(_ *http.Request) (*Backend, error) {
	healthy := s.pool.Healthy()
	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackend
	}

	// Counter over the healthy list, not the full one: skipping dead
	// instances by re-picking would give the instance after a dead one twice
	// the load. Subtract 1 so the first pick lands on index 0.
	index := (s.next.Add(1) - 1) % uint64(len(healthy))

	return healthy[index], nil
}
