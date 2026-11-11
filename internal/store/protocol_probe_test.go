package store

import (
	"testing"
)

// TestSaveProtocolProbeCompareAndSet 前置独立 PostgreSQL，验证探测只改元数据、拒绝旧版本/已删除部署及序列化错误。
// 辅助夹具清理专属 schema；不调用外部服务，不改变授权或预算。
func TestSaveProtocolProbeCompareAndSet(t *testing.T) {
	st, _ := openModelWeightStores(t)
	row := ProxyModel{ID: "probe-cas", ModelName: "test", Params: map[string]any{"model": "test", "api_key": "fake"}, Info: map[string]any{"id": "probe-cas", "auto_probe": true}}
	if err := st.UpsertProxyModel(row); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"verified": map[string]any{"openai-chat": true}}
	if ok, err := st.SaveProtocolProbe(t.Context(), row, record); err != nil || !ok {
		t.Fatalf("有效快照未更新: %v %v", ok, err)
	}
	if ok, err := st.SaveProtocolProbe(t.Context(), row, record); err != nil || ok {
		t.Fatalf("旧版本覆盖新数据: %v %v", ok, err)
	}
	rows, err := st.ListProxyModels()
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	saved := rows[0]
	if saved.Params["api_key"] != "fake" || saved.Info["protocol_probe"] == nil {
		t.Fatal("参数被改动或结果未保存")
	}
	if ok, err := st.SaveProtocolProbe(t.Context(), saved, map[string]any{"invalid": make(chan int)}); err == nil || ok {
		t.Fatal("序列化失败未阻断更新")
	}
	if err := st.DeleteProxyModel(row.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SaveProtocolProbe(t.Context(), saved, record); err != nil || ok {
		t.Fatalf("删除后被重建: %v %v", ok, err)
	}
}

// TestProtocolProbeLeaseAndPersistedSnapshot 验证单连接池仍能取得/释放租约、不同部署不互斥、过期接管及旧 owner 不能误删新租约。
// 数据库夹具自动删除 schema，快照使用原始持久字段而非运行时补字段。
func TestProtocolProbeLeaseAndPersistedSnapshot(t *testing.T) {
	st, _ := openModelWeightStores(t)
	st.DB.SetMaxOpenConns(1)
	acquire := func(id, owner string, want bool) {
		t.Helper()
		got, err := st.AcquireProtocolProbe(t.Context(), id, owner)
		if err != nil || got != want {
			t.Fatalf("租约 %s/%s got=%v want=%v err=%v", id, owner, got, want, err)
		}
	}
	acquire("a", "one", true)
	acquire("a", "two", false)
	acquire("b", "two", true)
	if _, err := st.DB.Exec(`UPDATE kv SET created_at=(clock_timestamp()-interval '151 seconds')::text WHERE kind='protocol_probe_lease' AND id='a'`); err != nil {
		t.Fatal(err)
	}
	acquire("a", "two", true)
	if err := st.ReleaseProtocolProbe(t.Context(), "a", "one"); err != nil {
		t.Fatal(err)
	}
	acquire("a", "three", false)
	if err := st.ReleaseProtocolProbe(t.Context(), "a", "two"); err != nil {
		t.Fatal(err)
	}
	acquire("a", "three", true)
	row := ProxyModel{ID: "persisted", ModelName: "test", Params: map[string]any{"model": "test"}, Info: map[string]any{"transport": "bypass_openai_chat"}}
	if err := st.UpsertProxyModel(row); err != nil {
		t.Fatal(err)
	}
	snapshot, err := st.ProtocolProbeModel(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Info["db_model"] != nil || snapshot.Info["id"] != nil {
		t.Fatal("快照注入了运行时字段")
	}
	if ok, err := st.SaveProtocolProbe(t.Context(), snapshot, map[string]any{"verified": true}); err != nil || !ok {
		t.Fatalf("原始快照无法保存: %v %v", ok, err)
	}
	if _, err := st.ProtocolProbeModel(t.Context(), "missing"); err == nil {
		t.Fatal("不存在的部署读成成功")
	}
}
