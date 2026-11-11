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
