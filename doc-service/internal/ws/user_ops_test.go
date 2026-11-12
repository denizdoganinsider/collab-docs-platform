package ws

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestRedisUserOpsCountsAcrossCallsWithTTL(t *testing.T) {
	addr := os.Getenv("DOCS_TEST_REDIS")
	if addr == "" {
		t.Skip("DOCS_TEST_REDIS not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	t.Cleanup(func() { client.FlushDB(context.Background()); client.Close() })
	ctx := context.Background()

	u := NewRedisUserOps(client, 3)
	for i := range 3 {
		if ok, err := u.Allow(ctx, 42); err != nil || !ok {
			t.Fatalf("op %d = %v, %v", i+1, ok, err)
		}
	}
	if ok, _ := u.Allow(ctx, 42); ok {
		t.Fatal("4th op admitted")
	}
	if ok, _ := u.Allow(ctx, 43); !ok {
		t.Fatal("another user refused")
	}
	if ttl := client.PTTL(ctx, "rl:wsops:42").Val(); ttl <= 0 {
		t.Fatalf("PTTL = %v, want an expiry", ttl)
	}
}
