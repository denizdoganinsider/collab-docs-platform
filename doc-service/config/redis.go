package config

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// NewRedis never exits: every Redis user in this process fails open, so a
// Redis that is down at boot is a degraded start, not a failed one. The
// ping runs here, before the listener opens, so the first request does not
// pay for the dial inside its own timeout.
func NewRedis(addr string) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  200 * time.Millisecond,
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
		PoolTimeout:  300 * time.Millisecond,
		MaxRetries:   -1,
		// Without it go-redis bounds socket I/O by Read/WriteTimeout only and
		// ignores a shorter context deadline from the caller.
		ContextTimeoutEnabled: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		slog.Warn("redis unreachable at startup; cache bypassed and rate limiters fail open until it returns", "addr", addr, "error", err)
	} else {
		slog.Info("redis connected", "addr", addr)
	}

	return client
}
