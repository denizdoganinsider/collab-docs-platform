package config

import (
	"testing"
	"time"
)

func TestNewRedisReturnsClientWhenRedisDown(t *testing.T) {
	start := time.Now()
	client := NewRedis("127.0.0.1:1")
	elapsed := time.Since(start)
	if client == nil {
		t.Fatal("NewRedis returned nil with Redis down")
	}
	t.Cleanup(func() { client.Close() })
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("NewRedis took %v with Redis down, want at most 1.5s", elapsed)
	}
}
