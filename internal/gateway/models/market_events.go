package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/sunqirui1987/xhub/internal/catalog"
)

// MarketEventNamespace 存放独立于站内信的待处理市场事件；后续投递方使用轮次 ID 去重。
const MarketEventNamespace = "market_change_events"

// marketEventTargetBytes 控制单条事件的通常大小；单条模型变化大于目标时完整保留，不裁剪报价。
const marketEventTargetBytes = 256 << 10

// MarketChange 表示一次来源目录的事实变化；价格仅为来源报价，不代表已部署模型的价格。
type MarketChange struct {
	Kind     string `json:"kind"`
	Model    string `json:"model"`
	Previous any    `json:"previous_price,omitempty"`
	Current  any    `json:"current_price,omitempty"`
}

// MarketChangeEvent 是一次成功同步产生的不可变事件，供后续投递模块消费。
// ID 为同步轮次，Time 为成功时间；Changes 按模型 ID 和变化类型稳定排序。
type MarketChangeEvent struct {
	ID      string         `json:"id"`
	Time    string         `json:"time"`
	Changes []MarketChange `json:"changes"`
}

// marketPriceJSON 对来源的完整价格规则做确定性编码；只对顶层规则排序，不改规则内数组语义。
// row 是来源模型行；ok 表示存在非空价格规则。供目录变化比较使用；编码异常返回错误。
func marketPriceJSON(row map[string]any) (encoded string, ok bool, err error) {
	value, exists := row["pricing_rules_v2"]
	if !exists || value == nil {
		return "", false, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", false, err
	}
	var rules []json.RawMessage
	if err := json.Unmarshal(raw, &rules); err != nil {
		return "", false, fmt.Errorf("invalid market pricing rules: %w", err)
	}
	if len(rules) == 0 {
		return "", false, nil
	}
	items := make([]string, 0, len(rules))
	for _, rule := range rules {
		var normalized any
		if err := json.Unmarshal(rule, &normalized); err != nil {
			return "", false, err
		}
		canonical, err := json.Marshal(normalized)
		if err != nil {
			return "", false, err
		}
		items = append(items, string(canonical))
	}
	sort.Strings(items)
	result, err := json.Marshal(items)
	return string(result), true, err
}

// diffMarketChanges 比较两次成功来源快照，prior 含保留的下架模型，current 是本轮原始来源。
// 返回有序变化或价格编码错误；由同步事务调用，不读取本地计费覆盖，不修改输入。
// 来源成功返回但缺失的模型按产品规则标记下架；来源未提供完整性证明，无法识别的部分返回是已知限制。
func diffMarketChanges(prior, current catalog.PriceDocument) ([]MarketChange, error) {
	changes := []MarketChange{}
	for id, row := range current.Models {
		if row["market_catalog"] != true {
			continue
		}
		old, existed := prior.Models[id]
		if !existed || old["market_catalog"] != true {
			changes = append(changes, MarketChange{Kind: "added", Model: id})
			continue
		}
		if old["feed_unavailable"] == true {
			changes = append(changes, MarketChange{Kind: "restored", Model: id})
		}
		before, beforeOK, err := marketPriceJSON(old)
		if err != nil {
			return nil, err
		}
		after, afterOK, err := marketPriceJSON(row)
		if err != nil {
			return nil, err
		}
		// 无报价无法推断价格上升或下降；单独记录资料增减，不把缺失当零。
		if beforeOK != afterOK {
			changes = append(changes, MarketChange{Kind: "price_information_changed", Model: id, Previous: old["pricing_rules_v2"], Current: row["pricing_rules_v2"]})
			continue
		}
		if beforeOK && afterOK && before != after {
			changes = append(changes, MarketChange{Kind: "price_changed", Model: id, Previous: old["pricing_rules_v2"], Current: row["pricing_rules_v2"]})
		}
	}
	for id, row := range prior.Models {
		if row["market_catalog"] != true || row["feed_unavailable"] == true {
			continue
		}
		if _, found := current.Models[id]; !found {
			changes = append(changes, MarketChange{Kind: "delisted", Model: id})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Model == changes[j].Model {
			return changes[i].Kind < changes[j].Kind
		}
		return changes[i].Model < changes[j].Model
	})
	return changes, nil
}

// recordMarketChanges 将成功同步的变化原子写入配置库，ctx/tx 来自市场事务，id 与 stamp 为本次轮次和成功时间。
// 首次持久化只建立基线；无变化不写事件。数据库或编码失败使调用事务回滚，投递由后续独立模块负责。
func recordMarketChanges(ctx context.Context, tx *sql.Tx, id, stamp string, prior, current catalog.PriceDocument) error {
	var hasPrior bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM proxy_config WHERE namespace=$1 AND key='catalog')`, marketSnapshotNS).Scan(&hasPrior); err != nil {
		return err
	}
	if !hasPrior {
		return nil
	}
	changes, err := diffMarketChanges(prior, current)
	if err != nil || len(changes) == 0 {
		return err
	}
	// 分片控制大量模型同时变化时的消息尺寸，稳定轮次加分片序号便于投递去重。
	var chunks [][]MarketChange
	var chunk []MarketChange
	used := 256
	for _, change := range changes {
		raw, err := json.Marshal(change)
		if err != nil {
			return err
		}
		if len(chunk) > 0 && used+len(raw)+1 > marketEventTargetBytes {
			chunks = append(chunks, chunk)
			chunk = nil
			used = 256
		}
		chunk = append(chunk, change)
		used += len(raw) + 1
	}
	if len(chunk) > 0 {
		chunks = append(chunks, chunk)
	}
	for index, items := range chunks {
		key := fmt.Sprintf("%s:%d", id, index)
		raw, err := json.Marshal(MarketChangeEvent{ID: key, Time: stamp, Changes: items})
		if err != nil {
			return err
		}
		// 事件一旦写入不得被同一键覆盖，消费方可安全地在事务外准备共享详情。
		if _, err := tx.ExecContext(ctx, `INSERT INTO proxy_config(namespace,key,value_json) VALUES($1,$2,$3) ON CONFLICT(namespace,key) DO NOTHING`, MarketEventNamespace, key, string(raw)); err != nil {
			return err
		}
	}
	return nil
}
