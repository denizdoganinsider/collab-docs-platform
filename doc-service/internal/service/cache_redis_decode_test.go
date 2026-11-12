package service

import (
	"context"
	"testing"
	"time"
)

func TestRedisDocListCacheUndecodableValueIsAnError(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	if err := client.Set(ctx, "doclist:5", "not-json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}

	got, hit, _, err := NewRedisDocListCache(client, time.Minute).Get(ctx, 5)
	if err == nil || hit || got != nil {
		t.Fatalf("Get = %#v hit=%v err=%v; want an error, not a hit or a miss", got, hit, err)
	}
}
