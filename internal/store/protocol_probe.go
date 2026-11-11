package store

import (
	"context"
	"encoding/json"
	"time"
)

// SaveProtocolProbe 仅更新指定部署的探测元数据，并以完整旧参数和元数据执行原子比较。
// 参数 ctx/expected/record 为请求期限、探测提交前快照与安全结果；返回是否更新及存储错误。
// 配置已被其他进程修改或删除时返回 false，不重建部署、不触碰权限、价格、预算或路由权重。
func (s *Store) SaveProtocolProbe(ctx context.Context, expected ProxyModel, record map[string]any) (bool, error) {
	params, err := json.Marshal(expected.Params)
	if err != nil {
		return false, err
	}
	info, err := json.Marshal(expected.Info)
	if err != nil {
		return false, err
	}
	result, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE proxy_models SET model_info_json=jsonb_set(model_info_json::jsonb, '{protocol_probe}', $1::jsonb)::text, updated_at=$2 WHERE id=$3 AND model_name=$4 AND litellm_params_json::jsonb=$5::jsonb AND model_info_json::jsonb=$6::jsonb`, string(result), time.Now().UTC().Format(time.RFC3339), expected.ID, expected.ModelName, string(params), string(info))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err == nil && n > 0 {
		s.bust(new(proxyModelRow))
	}
	return n == 1, err
}
