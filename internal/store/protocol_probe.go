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

// ProtocolProbeModel 读取未经运行时补字段的数据库快照；参数 ctx/id 为有界查询和模型行 ID，返回部署及查询错误。
// 供探测 CAS 使用，避免 db_model 等内存字段导致永远无法提交；不使用进程缓存。
func (s *Store) ProtocolProbeModel(ctx context.Context, id string) (ProxyModel, error) {
	row := ProxyModel{ID: id}
	var params, info string
	err := s.DB.QueryRowContext(ctx, `SELECT model_name, litellm_params_json, model_info_json FROM proxy_models WHERE id=$1`, id).Scan(&row.ModelName, &params, &info)
	if err != nil {
		return row, err
	}
	if err = json.Unmarshal([]byte(params), &row.Params); err != nil {
		return row, err
	}
	err = json.Unmarshal([]byte(info), &row.Info)
	return row, err
}

// AcquireProtocolProbe 以短 SQL 原子取得单部署租约，最长 150 秒；owner 为本次随机标识，返回获得标记和数据库错误。
// 网络探测最长 125 秒，不持有数据库连接或事务；进程崩溃后到期可接管，其他部署不互斥。
func (s *Store) AcquireProtocolProbe(ctx context.Context, id, owner string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `INSERT INTO kv(kind,id,body,created_at) VALUES('protocol_probe_lease',$1,$2,clock_timestamp()::text)
 ON CONFLICT(kind,id) DO UPDATE SET body=EXCLUDED.body,created_at=EXCLUDED.created_at
 WHERE kv.created_at::timestamptz < clock_timestamp()-interval '150 seconds'`, id, owner)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ReleaseProtocolProbe 只释放本次 owner 的租约，参数为有界清理上下文和 ID/owner；返回数据库错误，不删除后继实例的租约。
func (s *Store) ReleaseProtocolProbe(ctx context.Context, id, owner string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM kv WHERE kind='protocol_probe_lease' AND id=$1 AND body=$2`, id, owner)
	return err
}
