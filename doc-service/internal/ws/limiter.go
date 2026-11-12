package ws

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/time/rate"
)

// UserOps counts op frames per user across every connection and instance.
// The per-connection bucket cannot see a user spread over ten sockets on
// three instances; this can. False means the user's budget is spent.
type UserOps interface {
	Allow(ctx context.Context, userID int64) (bool, error)
}

// Limits are the read pump's frame limits.
type Limits struct {
	OpRate  float64 // op frames per second per connection
	OpBurst int
	// cursor frames per second per connection; the excess is dropped
	// silently, since a cursor is cosmetic.
	CursorRate float64
	// rate_limited answers within StrikeWindow that close the socket.
	Strikes      int
	StrikeWindow time.Duration
	// nil disables the cross-connection limit.
	UserOps UserOps
}

func DefaultLimits() Limits {
	return Limits{OpRate: 20, OpBurst: 40, CursorRate: 20, Strikes: 3, StrikeWindow: 10 * time.Second}
}

// userOpsTimeout bounds the Redis round trip one op frame waits for. Past it
// the frame is admitted: Redis is not allowed to sit on the ack path.
const userOpsTimeout = 100 * time.Millisecond

// frameLimiter is one connection's state. It is used only by that
// connection's read pump, so it needs no lock.
type frameLimiter struct {
	lim     Limits
	userID  int64
	ops     *rate.Limiter
	cursors *rate.Limiter
	strikes []time.Time
}

func newFrameLimiter(lim Limits, userID int64) *frameLimiter {
	return &frameLimiter{
		lim:     lim,
		userID:  userID,
		ops:     rate.NewLimiter(rate.Limit(lim.OpRate), lim.OpBurst),
		cursors: rate.NewLimiter(rate.Limit(lim.CursorRate), max(1, int(lim.CursorRate))),
	}
}

type verdict int

const (
	admit       verdict = iota
	drop                // silently
	rateLimited         // answer rate_limited, keep the socket
	closeSocket         // close 4429
)

func (f *frameLimiter) op(now time.Time) verdict {
	if !f.ops.AllowN(now, 1) {
		return f.strike(now)
	}
	if f.lim.UserOps != nil {
		ctx, cancel := context.WithTimeout(context.Background(), userOpsTimeout)
		ok, err := f.lim.UserOps.Allow(ctx, f.userID)
		cancel()
		if err != nil {
			slog.Warn("websocket user op limiter unavailable, failing open", "service", "doc-service", "user_id", f.userID, "error", err)
			return admit
		}
		if !ok {
			return closeSocket
		}
	}
	return admit
}

func (f *frameLimiter) cursor(now time.Time) verdict {
	if f.cursors.AllowN(now, 1) {
		return admit
	}
	return drop
}

// strike records one over-rate op frame and closes the socket on the
// Strikes-th within StrikeWindow: a client that keeps flooding after being
// told is not a fast typist.
func (f *frameLimiter) strike(now time.Time) verdict {
	cutoff := now.Add(-f.lim.StrikeWindow)
	kept := f.strikes[:0]
	for _, t := range f.strikes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	f.strikes = append(kept, now)
	if len(f.strikes) >= f.lim.Strikes {
		return closeSocket
	}
	return rateLimited
}
