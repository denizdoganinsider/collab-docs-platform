package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"collab-docs-platform/doc-service/internal/domain"
	"collab-docs-platform/doc-service/internal/session"

	"github.com/redis/go-redis/v9"
)

// DocListCache holds the first page of GET /documents per user. Get's
// boolean is the hit; an error means the cache could not answer, which the
// caller must not mistake for "this user has no documents". On a miss Get
// also returns a token naming the user's invalidation generation; Set with
// that token stores the list only if no invalidation ran in between, so a
// list read from the database before a write cannot overwrite that write's
// DEL.
type DocListCache interface {
	Get(ctx context.Context, userID int64) (docs []domain.DocumentSummary, hit bool, token string, err error)
	Set(ctx context.Context, userID int64, docs []domain.DocumentSummary, token string) error
	Invalidate(ctx context.Context, userIDs ...int64) error
}

// cacheTimeout bounds every cache round trip; past it the request goes to
// the database as if there were no cache.
const cacheTimeout = 200 * time.Millisecond

func cacheContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cacheTimeout)
}

// RedisDocListCache is DocListCache on the Redis every instance shares: the
// instance that changes a membership (hash-routed on the document) is not
// the one that serves the next list read (round-robined), and one DEL here
// is seen by all of them.
type RedisDocListCache struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisDocListCache(client *redis.Client, ttl time.Duration) *RedisDocListCache {
	return &RedisDocListCache{client: client, ttl: ttl}
}

func docListKey(userID int64) string { return "doclist:" + strconv.FormatInt(userID, 10) }

// docListGenKey counts a user's invalidations. It outlives any list read in
// flight by a wide margin; once it expires with no invalidation pending,
// "absent" compares equal to "absent" and nothing is lost.
func docListGenKey(userID int64) string { return "doclist:gen:" + strconv.FormatInt(userID, 10) }

const docListGenTTL = 10 * time.Minute

// setIfGenerationScript is the compare-and-set: the list is written only
// when the generation still equals the one read before the database query.
var setIfGenerationScript = redis.NewScript(`
local gen = redis.call('GET', KEYS[2]) or ''
if gen ~= ARGV[2] then
	return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
return 1
`)

func (c *RedisDocListCache) Get(ctx context.Context, userID int64) ([]domain.DocumentSummary, bool, string, error) {
	vals, err := c.client.MGet(ctx, docListKey(userID), docListGenKey(userID)).Result()
	if err != nil {
		return nil, false, "", err
	}
	token, _ := vals[1].(string)
	raw, ok := vals[0].(string)
	if !ok {
		return nil, false, token, nil
	}
	docs := make([]domain.DocumentSummary, 0)
	if err := json.Unmarshal([]byte(raw), &docs); err != nil {
		return nil, false, "", err
	}
	return docs, true, token, nil
}

// Set stores the empty list too: without it a user with no documents is a
// database read on every poll. It stores nothing when an invalidation ran
// after the Get that produced token.
func (c *RedisDocListCache) Set(ctx context.Context, userID int64, docs []domain.DocumentSummary, token string) error {
	if docs == nil {
		docs = []domain.DocumentSummary{}
	}
	raw, err := json.Marshal(docs)
	if err != nil {
		return err
	}
	keys := []string{docListKey(userID), docListGenKey(userID)}
	return setIfGenerationScript.Run(ctx, c.client, keys, raw, token, c.ttl.Milliseconds()).Err()
}

// Invalidate drops each list and moves its generation in one transaction,
// so a fill that read the database before this call can no longer land.
func (c *RedisDocListCache) Invalidate(ctx context.Context, userIDs ...int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	_, err := c.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		for _, id := range userIDs {
			p.Del(ctx, docListKey(id))
			p.Incr(ctx, docListGenKey(id))
			p.PExpire(ctx, docListGenKey(id), docListGenTTL)
		}
		return nil
	})
	return err
}

// invalidateUsers drops the given users' lists. A failure is logged and
// otherwise ignored: the write it follows has committed, and the TTL bounds
// how long a list can stay stale.
func invalidateUsers(cache DocListCache, reason string, userIDs ...int64) {
	if cache == nil {
		return
	}
	ctx, cancel := cacheContext()
	defer cancel()
	if err := cache.Invalidate(ctx, userIDs...); err != nil {
		slog.Warn("doclist invalidation failed; lists may be stale until the TTL", "service", "doc-service", "reason", reason, "user_ids", userIDs, "error", err)
	}
}

func memberIDs(members []domain.Member) []int64 {
	ids := make([]int64, len(members))
	for i, m := range members {
		ids[i] = m.UserID
	}
	return ids
}

// invalidateDocument drops the list of every member of docID, the owner
// included (the owner has a members row).
func invalidateDocument(cache DocListCache, members MemberRepositoryInterface, docID int64, reason string) {
	if cache == nil {
		return
	}
	list, err := members.List(docID)
	if err != nil {
		slog.Warn("doclist invalidation could not list members; lists may be stale until the TTL", "service", "doc-service", "reason", reason, "doc_id", docID, "error", err)
		return
	}
	invalidateUsers(cache, reason, memberIDs(list)...)
}

// InvalidatingStore is the session store with one addition: a snapshot
// moves documents.updated_at, which reorders every member's list, so every
// member's cached list goes. Ops alone do not touch the row, so a list may
// lag a document's last edit by up to one snapshot interval.
type InvalidatingStore struct {
	session.Store
	Members MemberRepositoryInterface
	Cache   DocListCache
}

func (s InvalidatingStore) Snapshot(docID int64, content string, version int64) error {
	if err := s.Store.Snapshot(docID, content, version); err != nil {
		return err
	}
	invalidateDocument(s.Cache, s.Members, docID, "snapshot")
	return nil
}
