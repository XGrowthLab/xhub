package storageaudit

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/sunqirui1987/xhub/internal/logframe"
)

// TestRecoverDiagnosticOriginal 在真实 PostgreSQL 中损坏压缩载荷并保留版本头。
// 核对备份身份、原始字节、失败后的事务状态及异步写入者的新版；临时 schema 随用例删除。
func TestRecoverDiagnosticOriginal(t *testing.T) {
	ctx := t.Context()
	db := fixture(t)
	raw := diagnosticFixture(t, 3)
	const requestID = "verified-original-fixture"
	proof, err := ReadPreimages(strings.NewReader(fmt.Sprintf("%x %x\n",
		sha256.Sum256([]byte(requestID)), sha256.Sum256([]byte(raw)))))
	if err != nil {
		t.Fatal(err)
	}
	var event int64
	if err = db.QueryRow("INSERT INTO usage_events(request_id,owner_type,cost) VALUES($1,'personal',2.5) RETURNING id", requestID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	frame, err := logframe.Encode(raw)
	if err != nil {
		t.Fatal(err)
	}
	damaged := frame[:strings.LastIndexByte(frame, ':')+1] + "!"
	if _, err = db.Exec("INSERT INTO request_logs(request_id,upstream_response,error) VALUES($1,$2,'original error')", requestID, damaged); err != nil {
		t.Fatal(err)
	}

	for _, invalid := range []struct {
		n        int64
		body     string
		manifest *Preimages
	}{
		{0, raw, proof}, {event, "[]", proof}, {event, raw, nil}, {event, "{}", proof},
		{event, raw, &Preimages{hashes: map[string][sha256.Size]byte{}}},
	} {
		if ok, e := RecoverDiagnostic(ctx, db, invalid.n, []byte(invalid.body), invalid.manifest); e == nil || ok {
			t.Fatal("错误原件或清单被允许恢复日志")
		}
	}
	if ok, e := RecoverDiagnostic(ctx, db, event+1000, []byte(raw), proof); e != nil || ok {
		t.Fatal("不存在的日志被误报为恢复", e)
	}

	// 触发器模拟数据库实际保存了与请求不同的值，独立读回必须发现错误并回滚整条记录。
	if _, err = db.Exec("CREATE FUNCTION alter_original() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.upstream_response='unexpected replacement'; RETURN NEW; END $$; CREATE TRIGGER alter_original BEFORE UPDATE ON request_logs FOR EACH ROW EXECUTE FUNCTION alter_original()"); err != nil {
		t.Fatal(err)
	}
	if ok, e := RecoverDiagnostic(ctx, db, event, []byte(raw), proof); e == nil || ok {
		t.Fatal("未检出写后原件损坏")
	}
	var stored string
	if err = db.QueryRow("SELECT upstream_response FROM request_logs WHERE request_id=$1", requestID).Scan(&stored); err != nil || stored != damaged {
		t.Fatal("失败恢复未回滚到原有帧", err)
	}
	if _, err = db.Exec("DROP TRIGGER alter_original ON request_logs"); err != nil {
		t.Fatal(err)
	}
	if ok, e := RecoverDiagnostic(ctx, db, event, []byte(raw), proof); e != nil || !ok {
		t.Fatal("真实原件无法修复损坏载荷", e)
	}
	var equal bool
	if err = db.QueryRow("SELECT l.upstream_response=$2 AND l.error='original error' AND u.cost=2.5 FROM request_logs l JOIN usage_events u USING(request_id) WHERE l.request_id=$1", requestID, raw).Scan(&equal); err != nil || !equal {
		t.Fatal("恢复没有保留字节、错误检索或费用", err)
	}
	if ok, e := RecoverDiagnostic(ctx, db, event, []byte(raw), proof); e != nil || ok {
		t.Fatal("重复运行改写了已恢复的日志", e)
	}

	newer, err := logframe.Encode(raw + " ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE request_logs SET upstream_response=$1 WHERE request_id=$2", newer, requestID); err != nil {
		t.Fatal(err)
	}
	if ok, e := RecoverDiagnostic(ctx, db, event, []byte(raw), proof); e != nil || ok {
		t.Fatal("旧恢复点覆盖了异步任务的新版本", e)
	}
	if err = db.QueryRow("SELECT upstream_response FROM request_logs WHERE request_id=$1", requestID).Scan(&stored); err != nil || stored != newer {
		t.Fatal("拒绝过期恢复后新版字节改变", err)
	}
}
