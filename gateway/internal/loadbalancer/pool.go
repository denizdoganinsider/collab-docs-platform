package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const healthCheckTimeout = 2 * time.Second

// ErrNoHealthyBackend means every instance is down.
var ErrNoHealthyBackend = errors.New("no healthy doc-service instance")

// Pool is the static set of doc-service instances plus the goroutine that
// keeps their health flags current. Membership never changes after
// construction - instances are configured, not discovered - which is what
// lets ConsistentHash build its ring once and never rebuild it.
type Pool struct {
	backends   []*Backend
	httpClient *http.Client
	stop       chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

func NewPool(rawURLs []string) (*Pool, error) {
	if len(rawURLs) == 0 {
		return nil, errors.New("at least one doc-service URL is required")
	}

	backends := make([]*Backend, 0, len(rawURLs))
	seen := make(map[string]bool, len(rawURLs))
	for _, rawURL := range rawURLs {
		b, err := NewBackend(rawURL)
		if err != nil {
			return nil, fmt.Errorf("invalid doc-service URL: %w", err)
		}
		// A duplicate would get two ring segments and two round-robin turns.
		if seen[b.URL.Host] {
			return nil, fmt.Errorf("duplicate doc-service URL %q", rawURL)
		}
		seen[b.URL.Host] = true
		backends = append(backends, b)
	}

	return &Pool{
		backends:   backends,
		httpClient: &http.Client{Timeout: healthCheckTimeout},
		stop:       make(chan struct{}),
	}, nil
}

func (p *Pool) Backends() []*Backend {
	return p.backends
}

// Healthy returns the instances currently passing their health check, in
// configuration order. The slice is freshly built on every call; it is small.
func (p *Pool) Healthy() []*Backend {
	healthy := make([]*Backend, 0, len(p.backends))
	for _, b := range p.backends {
		if b.Healthy() {
			healthy = append(healthy, b)
		}
	}

	return healthy
}

// Start runs one health sweep synchronously - so the very first request
// already sees the truth - then keeps sweeping every interval until Close.
func (p *Pool) Start(interval time.Duration) {
	p.Check()

	p.wg.Go(func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.Check()
			}
		}
	})
}

func (p *Pool) Close() {
	p.stopOnce.Do(func() { close(p.stop) })
	p.wg.Wait()
}

// Check probes every instance once, in parallel: one slow backend must not
// delay noticing that another has died. Exported so tests can force a sweep.
func (p *Pool) Check() {
	var wg sync.WaitGroup
	for _, b := range p.backends {
		wg.Go(func() { p.check(b) })
	}
	wg.Wait()
}

func (p *Pool) check(b *Backend) {
	healthy, reason := p.probe(b)

	if b.setHealthy(healthy) {
		if healthy {
			slog.Info("doc-service instance is back", "service", "gateway", "backend", b.String())
		} else {
			slog.Warn("doc-service instance is down", "service", "gateway", "backend", b.String(), "reason", reason)
		}
	}
}

// probe is the one request the balancer makes on its own. /health on
// doc-service is static plus a draining flag (month 3, item 5): it answers
// 503 from the moment SIGTERM arrives, which is how the instance leaves the
// healthy list before its listener closes.
func (p *Pool) probe(b *Backend) (healthy bool, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), healthCheckTimeout)
	defer cancel()

	target := b.URL.JoinPath("/health").String()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err.Error()
	}

	response, err := p.httpClient.Do(request)
	if err != nil {
		return false, err.Error()
	}
	// Drain so the keep-alive connection is reused by the next probe.
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return false, fmt.Sprintf("status=%d", response.StatusCode)
	}

	return true, ""
}
