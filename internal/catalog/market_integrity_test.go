package catalog

import "testing"

// TestMarketRejectsInvalidIDs 验证空模型 ID 或重复 ID 时整轮失败，避免丢失条目造成误下架；无资源需要清理。
func TestMarketRejectsInvalidIDs(t *testing.T) {
	for _, raw := range []string{`{"status":true,"data":[{"id":"a"},{"id":""}]}`, `{"status":true,"data":[{"id":"a"},{"id":"a"}]}`} {
		if _, err := BuildPriceDocument([]byte(raw)); err == nil {
			t.Fatalf("不完整来源被接受: %s", raw)
		}
	}
}
