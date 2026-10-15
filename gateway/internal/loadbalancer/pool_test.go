package loadbalancer

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// healthServer answers /health with whatever status is currently loaded.
func healthServer(t *testing.T, status *atomic.Int32) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	t.Cleanup(server.Close)

	return server
}

func TestStartRunsAFirstCheckSynchronously(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	server := healthServer(t, &status)

	pool := newTestPool(t, server.URL)
	pool.Start(time.Hour)
	defer pool.Close()

	// No sleep: Start must have already flipped the optimistic default.
	if pool.Backends()[0].Healthy() {
		t.Error("backend still marked healthy after a failing synchronous check")
	}
}

func TestHealthCheckFlipsBothWays(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := healthServer(t, &status)

	pool := newTestPool(t, server.URL)
	pool.Start(20 * time.Millisecond)
	defer pool.Close()

	b := pool.Backends()[0]
	if !b.Healthy() {
		t.Fatal("backend should be healthy after a 200")
	}

	status.Store(http.StatusServiceUnavailable)
	waitFor(t, func() bool { return !b.Healthy() }, "backend to be marked down")

	status.Store(http.StatusOK)
	waitFor(t, func() bool { return b.Healthy() }, "backend to be marked back up")
}

func TestUnreachableInstanceIsDown(t *testing.T) {
	server := healthServer(t, new(atomic.Int32))
	url := server.URL
	server.Close() // nothing listens here any more

	pool := newTestPool(t, url)
	pool.Start(time.Hour)
	defer pool.Close()

	if pool.Backends()[0].Healthy() {
		t.Error("connection refused was not treated as unhealthy")
	}
	if len(pool.Healthy()) != 0 {
		t.Error("Healthy() still lists the dead instance")
	}
}

func TestCloseStopsThePoller(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	server := healthServer(t, &status)

	pool := newTestPool(t, server.URL)
	pool.Start(10 * time.Millisecond)
	pool.Close()

	status.Store(http.StatusServiceUnavailable)
	time.Sleep(50 * time.Millisecond)
	if !pool.Backends()[0].Healthy() {
		t.Error("a probe ran after Close")
	}
}

func waitFor(t *testing.T, condition func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %s", what)
}
