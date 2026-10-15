// Package loadbalancer picks which doc-service instance a request goes to.
//
// Two strategies, because the two kinds of request behind the gateway are not
// the same problem. User-scoped HTTP (list and create documents) is a database
// read any instance can answer, so RoundRobin spreads it evenly. Everything
// about one document is sticky: the OT session is in-memory and per-process,
// so the socket, the live reads and the membership changes for a document
// MUST all land on the same instance. ConsistentHash maps a document id to
// one instance, deterministically, with no shared state between the
// instances.
package loadbalancer

import (
	"fmt"
	"net/url"
	"sync/atomic"
)

// Backend is one doc-service instance. Health is an atomic flag flipped by
// the poller and read on every pick, so the hot path never takes a lock.
type Backend struct {
	URL     *url.URL
	healthy atomic.Bool
}

func NewBackend(rawURL string) (*Backend, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	// url.Parse accepts "doc-service:9001" (scheme "doc-service", no host)
	// without complaint; the proxy would start, report healthy, and 502
	// every request. Fail at startup instead, like a missing secret does.
	if (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return nil, fmt.Errorf("%q must be an http(s) URL with a host", rawURL)
	}

	b := &Backend{URL: target}
	// Optimistic until the first check says otherwise: Pool.Start runs that
	// check synchronously before the gateway accepts traffic, so a dead
	// instance is never picked on the strength of this default.
	b.healthy.Store(true)

	return b, nil
}

func (b *Backend) Healthy() bool {
	return b.healthy.Load()
}

func (b *Backend) setHealthy(healthy bool) (changed bool) {
	return b.healthy.Swap(healthy) != healthy
}

// String is the host:port only - what the logs need to prove which instance
// served a request, and nothing more.
func (b *Backend) String() string {
	return b.URL.Host
}
