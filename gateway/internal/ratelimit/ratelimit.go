// Package ratelimit holds the gateway's two Redis-backed limiters and the
// middleware that applies them. Both run as one Lua script per request, so
// a check and its update are one atomic step on the shared store that every
// gateway instance sees.
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// Decision is one limiter answer. RetryAfter is set only when Allowed is
// false: how long until the next request would be admitted.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type Limiter interface {
	Allow(ctx context.Context, key string) (Decision, error)
}

// slidingWindowScript keeps one sorted-set member per admitted request,
// scored by the Redis server clock in microseconds. The clock is the
// server's so that gateways with drifting clocks share one timeline.
const slidingWindowScript = `
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - window)
local count = redis.call('ZCARD', KEYS[1])
if count < limit then
	redis.call('ZADD', KEYS[1], now, now .. '-' .. ARGV[3])
	redis.call('PEXPIRE', KEYS[1], math.ceil(window / 1000))
	return {1, 0}
end
local oldest = redis.call('ZRANGE', KEYS[1], 0, 0, 'WITHSCORES')
return {0, tonumber(oldest[2]) + window - now}
`

// SlidingWindow is the sliding window log: exactly Limit requests in any
// Window-long interval, at the cost of one set member per admitted request.
type SlidingWindow struct {
	client *redis.Client
	script *redis.Script
	prefix string
	limit  int
	window time.Duration
}

func NewSlidingWindow(client *redis.Client, prefix string, limit int, window time.Duration) *SlidingWindow {
	return &SlidingWindow{client: client, script: redis.NewScript(slidingWindowScript), prefix: prefix, limit: limit, window: window}
}

func (l *SlidingWindow) Allow(ctx context.Context, key string) (Decision, error) {
	res, err := l.script.Run(ctx, l.client, []string{l.prefix + key}, l.window.Microseconds(), l.limit, nonce()).Int64Slice()
	if err != nil {
		return Decision{}, err
	}
	if res[0] == 1 {
		return Decision{Allowed: true}, nil
	}
	return Decision{RetryAfter: time.Duration(res[1]) * time.Microsecond}, nil
}

// fixedWindowScript sets the expiry in the same step as the increment, and
// repairs a key that somehow lost it: a counter without a TTL never resets
// and locks its key out for good (prevention rule SD-154318).
const fixedWindowScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 or redis.call('PTTL', KEYS[1]) < 0 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {count, redis.call('PTTL', KEYS[1])}
`

// FixedWindow is INCR with an expiry: at most Limit requests per window that
// starts with the first one. Up to 2x Limit can pass across a boundary.
type FixedWindow struct {
	client *redis.Client
	script *redis.Script
	prefix string
	limit  int
	window time.Duration
}

func NewFixedWindow(client *redis.Client, prefix string, limit int, window time.Duration) *FixedWindow {
	return &FixedWindow{client: client, script: redis.NewScript(fixedWindowScript), prefix: prefix, limit: limit, window: window}
}

func (l *FixedWindow) Allow(ctx context.Context, key string) (Decision, error) {
	res, err := l.script.Run(ctx, l.client, []string{l.prefix + key}, l.window.Milliseconds()).Int64Slice()
	if err != nil {
		return Decision{}, err
	}
	if res[0] <= int64(l.limit) {
		return Decision{Allowed: true}, nil
	}
	return Decision{RetryAfter: time.Duration(res[1]) * time.Millisecond}, nil
}

// nonce makes two requests in the same microsecond two set members.
func nonce() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
