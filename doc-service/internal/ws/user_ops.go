package ws

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// userOpsScript increments and sets the expiry in one step, so a counter
// can never outlive its window without a TTL (prevention rule SD-154318).
const userOpsScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 or redis.call('PTTL', KEYS[1]) < 0 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`

// RedisUserOps is a fixed window per user on the shared Redis.
type RedisUserOps struct {
	client *redis.Client
	script *redis.Script
	limit  int64
	window time.Duration
}

func NewRedisUserOps(client *redis.Client, perMinute int) *RedisUserOps {
	return &RedisUserOps{client: client, script: redis.NewScript(userOpsScript), limit: int64(perMinute), window: time.Minute}
}

func (u *RedisUserOps) Allow(ctx context.Context, userID int64) (bool, error) {
	count, err := u.script.Run(ctx, u.client, []string{"rl:wsops:" + strconv.FormatInt(userID, 10)}, u.window.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return count <= u.limit, nil
}
