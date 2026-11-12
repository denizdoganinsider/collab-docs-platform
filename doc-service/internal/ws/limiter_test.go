package ws

import (
	"context"
	"errors"
	"testing"
	"time"
)

type stubUserOps struct {
	allow bool
	err   error
	calls int
}

func (s *stubUserOps) Allow(context.Context, int64) (bool, error) {
	s.calls++
	return s.allow, s.err
}

func TestFrameLimiterOpBurstThenStrikesThenClose(t *testing.T) {
	lim := DefaultLimits()
	f := newFrameLimiter(lim, 1)
	now := time.Unix(1_000, 0)

	for i := range lim.OpBurst {
		if v := f.op(now); v != admit {
			t.Fatalf("op %d within burst = %v, want admit", i+1, v)
		}
	}
	if v := f.op(now); v != rateLimited {
		t.Fatalf("first op over burst = %v, want rateLimited", v)
	}
	if v := f.op(now); v != rateLimited {
		t.Fatalf("second op over burst = %v, want rateLimited", v)
	}
	if v := f.op(now); v != closeSocket {
		t.Fatalf("third strike = %v, want closeSocket", v)
	}
}

func TestFrameLimiterStrikesExpireOutsideWindow(t *testing.T) {
	lim := DefaultLimits()
	lim.OpBurst = 1
	lim.OpRate = 0.001
	f := newFrameLimiter(lim, 1)
	start := time.Unix(1_000, 0)

	f.op(start) // spends the burst
	if v := f.op(start); v != rateLimited {
		t.Fatalf("strike 1 = %v", v)
	}
	if v := f.op(start.Add(time.Second)); v != rateLimited {
		t.Fatalf("strike 2 = %v", v)
	}
	// The first two strikes are older than StrikeWindow now.
	if v := f.op(start.Add(lim.StrikeWindow + 2*time.Second)); v != rateLimited {
		t.Fatalf("strike after window = %v, want rateLimited (old strikes expired)", v)
	}
}

func TestFrameLimiterRefillsAtRate(t *testing.T) {
	lim := DefaultLimits()
	f := newFrameLimiter(lim, 1)
	now := time.Unix(1_000, 0)
	for range lim.OpBurst {
		f.op(now)
	}
	// One second refills OpRate tokens.
	later := now.Add(time.Second)
	for i := range int(lim.OpRate) {
		if v := f.op(later); v != admit {
			t.Fatalf("op %d after refill = %v, want admit", i+1, v)
		}
	}
}

func TestFrameLimiterCursorDropsSilently(t *testing.T) {
	f := newFrameLimiter(DefaultLimits(), 1)
	now := time.Unix(1_000, 0)
	admitted, dropped := 0, 0
	for range 100 {
		switch f.cursor(now) {
		case admit:
			admitted++
		case drop:
			dropped++
		default:
			t.Fatal("cursor frames must only be admitted or dropped")
		}
	}
	if admitted != 20 || dropped != 80 {
		t.Fatalf("admitted %d dropped %d, want 20 and 80", admitted, dropped)
	}
	if len(f.strikes) != 0 {
		t.Fatalf("dropped cursors counted as strikes: %d", len(f.strikes))
	}
}

func TestFrameLimiterUserOpsExceededCloses(t *testing.T) {
	lim := DefaultLimits()
	lim.UserOps = &stubUserOps{allow: false}
	if v := newFrameLimiter(lim, 1).op(time.Unix(1_000, 0)); v != closeSocket {
		t.Fatalf("user budget spent = %v, want closeSocket", v)
	}
}

func TestFrameLimiterUserOpsErrorFailsOpen(t *testing.T) {
	lim := DefaultLimits()
	lim.UserOps = &stubUserOps{err: errors.New("redis down")}
	if v := newFrameLimiter(lim, 1).op(time.Unix(1_000, 0)); v != admit {
		t.Fatalf("user limiter error = %v, want admit (fail open)", v)
	}
}

func TestFrameLimiterOverRateSkipsUserOps(t *testing.T) {
	lim := DefaultLimits()
	lim.OpBurst = 1
	stub := &stubUserOps{allow: true}
	lim.UserOps = stub
	f := newFrameLimiter(lim, 1)
	now := time.Unix(1_000, 0)
	f.op(now)
	f.op(now)
	if stub.calls != 1 {
		t.Fatalf("user counter calls = %d, want 1 (a locally refused frame is not counted)", stub.calls)
	}
}
