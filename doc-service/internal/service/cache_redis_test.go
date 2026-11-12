package service

import (
	"context"
	"os"
	"testing"
	"time"

	"collab-docs-platform/doc-service/internal/domain"

	"github.com/redis/go-redis/v9"
)

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("DOCS_TEST_REDIS")
	if addr == "" {
		t.Skip("DOCS_TEST_REDIS not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis at %s: %v", addr, err)
	}
	t.Cleanup(func() { client.FlushDB(context.Background()); client.Close() })
	return client
}

func TestRedisDocListCacheRoundTrip(t *testing.T) {
	client := testRedis(t)
	c := NewRedisDocListCache(client, time.Minute)
	ctx := context.Background()

	_, hit, token, err := c.Get(ctx, 1)
	if hit || err != nil {
		t.Fatalf("empty cache: hit=%v err=%v", hit, err)
	}
	want := []domain.DocumentSummary{{ID: 7, Title: "a", Role: domain.RoleOwner}}
	if err := c.Set(ctx, 1, want, token); err != nil {
		t.Fatal(err)
	}
	got, hit, _, err := c.Get(ctx, 1)
	if err != nil || !hit || len(got) != 1 || got[0].ID != 7 || got[0].Role != domain.RoleOwner {
		t.Fatalf("Get = %+v hit=%v err=%v", got, hit, err)
	}
	if ttl := client.TTL(ctx, "doclist:1").Val(); ttl <= 0 || ttl > time.Minute {
		t.Fatalf("TTL = %v", ttl)
	}
}

func TestRedisDocListCacheStoresEmptyListAsHit(t *testing.T) {
	c := NewRedisDocListCache(testRedis(t), time.Minute)
	ctx := context.Background()
	if err := c.Set(ctx, 2, nil, ""); err != nil {
		t.Fatal(err)
	}
	got, hit, _, err := c.Get(ctx, 2)
	if err != nil || !hit || got == nil || len(got) != 0 {
		t.Fatalf("Get = %#v hit=%v err=%v; want a non-nil empty hit", got, hit, err)
	}
}

func TestRedisDocListCacheInvalidateManyUsers(t *testing.T) {
	c := NewRedisDocListCache(testRedis(t), time.Minute)
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		_ = c.Set(ctx, id, nil, "")
	}
	if err := c.Invalidate(ctx, 1, 3); err != nil {
		t.Fatal(err)
	}
	for id, wantHit := range map[int64]bool{1: false, 2: true, 3: false} {
		if _, hit, _, _ := c.Get(ctx, id); hit != wantHit {
			t.Fatalf("user %d hit=%v, want %v", id, hit, wantHit)
		}
	}
	if err := c.Invalidate(ctx); err != nil {
		t.Fatalf("Invalidate with no ids = %v", err)
	}
}

func TestRedisDocListCacheErrorWhenRedisDown(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "localhost:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer client.Close()
	if _, hit, _, err := NewRedisDocListCache(client, time.Minute).Get(context.Background(), 1); err == nil || hit {
		t.Fatalf("Redis down: hit=%v err=%v; want an error, not a miss", hit, err)
	}
}

// A list read from the database before a concurrent write must not land
// after that write's invalidation: the share would stay invisible for the
// whole TTL.
func TestRedisDocListCacheFillLosesToConcurrentInvalidation(t *testing.T) {
	c := NewRedisDocListCache(testRedis(t), time.Minute)
	ctx := context.Background()

	_, _, token, err := c.Get(ctx, 4) // miss: the reader goes to the database
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Invalidate(ctx, 4); err != nil { // the write lands meanwhile
		t.Fatal(err)
	}
	stale := []domain.DocumentSummary{{ID: 1, Title: "before the share"}}
	if err := c.Set(ctx, 4, stale, token); err != nil {
		t.Fatal(err)
	}
	if _, hit, _, _ := c.Get(ctx, 4); hit {
		t.Fatal("a fill read before the invalidation was stored after it")
	}

	_, _, fresh, _ := c.Get(ctx, 4)
	if err := c.Set(ctx, 4, stale, fresh); err != nil {
		t.Fatal(err)
	}
	if _, hit, _, _ := c.Get(ctx, 4); !hit {
		t.Fatal("a fill with the current generation was not stored")
	}
}

func TestRedisDocListCacheInvalidateBoundsTheGenerationKey(t *testing.T) {
	client := testRedis(t)
	c := NewRedisDocListCache(client, time.Minute)
	ctx := context.Background()
	if err := c.Invalidate(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if ttl := client.PTTL(ctx, "doclist:gen:6").Val(); ttl <= 0 || ttl > docListGenTTL {
		t.Fatalf("generation PTTL = %v, want (0, %v]", ttl, docListGenTTL)
	}
}
