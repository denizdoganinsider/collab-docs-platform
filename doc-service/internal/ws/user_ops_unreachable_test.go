package ws

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisUserOpsErrorsWhenRedisUnreachable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "localhost:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer client.Close()

	ok, err := NewRedisUserOps(client, 3).Allow(context.Background(), 42)
	if err == nil {
		t.Fatalf("Allow with Redis unreachable = %v, nil; want an error so the limiter fails open", ok)
	}
}
