package live

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// ReserveDeliveryRecovery 在持有数据库标记行锁时，原子竞争请求的未知结果恢复权。
// ctx 控制 Redis 调用，ids 是已过期且没有数据库终态的请求；返回允许恢复的 ID 集合或错误。
// 与确认入队共用永久去重 hash：已入队的确认优先，恢复先赢时拒绝迟到确认；数据库失败可重试相同恢复权。
// 不保存正文、不追加账单、不调整热费用。旧队列缺少去重索引时仍核对队列，损坏内容则保留全部标记。
func (c *Client) ReserveDeliveryRecovery(ctx context.Context, ids []string) (map[string]bool, error) {
	if c == nil || c.rdb == nil {
		return nil, fmt.Errorf("delivery recovery: Redis client is unavailable")
	}
	allowed := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return allowed, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		if id == "" {
			return nil, fmt.Errorf("delivery recovery: request_id is required")
		}
		args[i] = id
	}
	reserved, err := reserveDeliveryRecoveryScript.Run(ctx, c.rdb, []string{"xhub:spendlog", "xhub:spend:requests"}, args...).StringSlice()
	if err != nil {
		return nil, err
	}
	for _, id := range reserved {
		allowed[id] = true
	}
	return allowed, nil
}

// reserveDeliveryRecoveryScript 与 enqueueSpendScript 在同一个 Redis 中原子执行。
// 先完整检查再写入，避免 Lua 异常留下部分恢复权；批量候选只扫描队列一次，兼容缺失索引的历史记录。
// 恢复标识不设 TTL：数据库回滚、重启或迟到写入不能绕过去重。它沿用现有账单去重 hash 的持久化要求。
var reserveDeliveryRecoveryScript = redis.NewScript(`
local candidates = {}
local count = 0
for _, id in ipairs(ARGV) do
  local state = redis.call('HGET', KEYS[2], id)
  if not state or state == 'delivery_recovery' then
    candidates[id] = true
    count = count + 1
  end
end
if count == 0 then return {} end
for _, raw in ipairs(redis.call('LRANGE', KEYS[1], 0, -1)) do
  local ok, row = pcall(cjson.decode, raw)
  if not ok or type(row) ~= 'table' or type(row.request_id) ~= 'string' or row.request_id == '' then
    return redis.error_reply('invalid spend queue during delivery recovery')
  end
  candidates[row.request_id] = nil
end
local reserved = {}
for _, id in ipairs(ARGV) do
  if candidates[id] then
    redis.call('HSET', KEYS[2], id, 'delivery_recovery')
    table.insert(reserved, id)
    candidates[id] = nil
  end
end
return reserved
`)
