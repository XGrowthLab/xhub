package store

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// TestCompactHistorySubmittedBlocks 使用两个认证范围各自的 32 轮合成工具会话验证实际提交的内容块。
// PostgreSQL 的 BEFORE INSERT 可观察冲突忽略前的候选；同时比较续聊字节、读写 P95。退出回收触发器和私有 schema。
func TestCompactHistorySubmittedBlocks(t *testing.T) {
	s, _ := openModelWeightStores(t)
	ctx := t.Context()
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE history_submissions (payload_bytes integer NOT NULL);
      CREATE FUNCTION record_submission() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
       INSERT INTO history_submissions VALUES (octet_length(NEW.payload)); RETURN NEW; END $$;
      CREATE TRIGGER observe_candidates BEFORE INSERT ON conversation_history_blocks FOR EACH ROW EXECUTE FUNCTION record_submission()`); err != nil {
		t.Fatal(err)
	}
	const turns = 32
	var writes, reads []time.Duration
	for session := 0; session < 2; session++ {
		// 两个范围使用同一批相同内容，确保不会利用另一个范围的命中跳过本范围的写入。
		random := rand.New(rand.NewSource(315))
		var items []map[string]string
		scope := fmt.Sprintf("scope-%d", session)
		for turn := 0; turn < turns; turn++ {
			raw := make([]byte, 8192)
			if _, err := random.Read(raw); err != nil {
				t.Fatal(err)
			}
			items = append(items, map[string]string{"type": "function_call_output", "call_id": fmt.Sprint(turn), "output": hex.EncodeToString(raw)})
			value, err := json.Marshal(items)
			if err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("response-%d-%d", session, turn)
			start := time.Now()
			if err = s.WriteHistory(ctx, key, scope, string(value), true); err != nil {
				t.Fatal(err)
			}
			writes = append(writes, time.Since(start))
			start = time.Now()
			got, err := s.ReadHistory(ctx, key, scope)
			reads = append(reads, time.Since(start))
			if err != nil || got != string(value) {
				t.Fatalf("范围 %d 第 %d 轮续聊丢失: %v", session, turn, err)
			}
		}
	}
	var attempts, bytes, stored int64
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*),sum(payload_bytes),(SELECT sum(octet_length(payload)) FROM conversation_history_blocks)
       FROM history_submissions`).Scan(&attempts, &bytes, &stored); err != nil {
		t.Fatal(err)
	}
	slices.Sort(writes)
	slices.Sort(reads)
	p95 := (len(writes)*95+99)/100 - 1
	t.Logf("turns=%d candidates=%d candidate_payload_bytes=%d unique_payload_bytes=%d write_p95=%s read_p95=%s", len(writes), attempts, bytes, stored, writes[p95], reads[p95])
	if attempts != turns*2 || bytes != stored {
		t.Fatalf("重复历史仍提交给 PostgreSQL，候选 %d，预期 %d", attempts, turns*2)
	}
}
