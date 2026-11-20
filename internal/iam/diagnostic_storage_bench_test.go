package iam

import (
	"crypto/sha256"
	"fmt"
	"github.com/sunqirui1987/xhub/cmd/regression/testsupport"
	"github.com/sunqirui1987/xhub/internal/logframe"
	"slices"
	"strings"
	"testing"
	"time"
)

// BenchmarkDiagnosticDetail 比较相同原文的 PostgreSQL 普通详情与压缩详情，输出实际字段占用和客户端读回 P95。
// 每轮各读取 40 次，先预热，两种版本交替读取以减少缓存偏差。仅使用合成诊断，结束时回收私有 schema。
func BenchmarkDiagnosticDetail(b *testing.B) {
	dsn := testsupport.Postgres(b, "diagdetailbench")
	ctx := b.Context()
	db, err := Open(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	var token strings.Builder
	for i := 0; i < 768; i++ {
		fmt.Fprintf(&token, "%x", sha256.Sum256([]byte(fmt.Sprint(i))))
	}
	raw := `{"counters":"` + strings.Repeat(token.String(), 4) + `"}`
	packed, err := logframe.Encode(raw)
	if err != nil {
		b.Fatal(err)
	}
	type observations struct {
		id, value string
		latencies []time.Duration
		stored    int64
	}
	records := []observations{{id: "plain-diag", value: raw}, {id: "packed-diag", value: packed}}
	for i := range records {
		r := &records[i]
		if _, err = db.Engine.Exec("INSERT INTO usage_events(request_id,owner_type) VALUES(?,'personal')", r.id); err != nil {
			b.Fatal(err)
		}
		if _, err = db.Engine.Exec("INSERT INTO request_logs(request_id,upstream_response) VALUES(?,?)", r.id, r.value); err != nil {
			b.Fatal(err)
		}
		if _, err = db.Engine.SQL("SELECT pg_column_size(upstream_response) FROM request_logs WHERE request_id=?", r.id).Get(&r.stored); err != nil {
			b.Fatal(err)
		}
		if _, err = db.GetRequestLog(ctx, r.id); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for n := 0; n < b.N*40; n++ {
		for j := range records {
			r := &records[(n+j)%len(records)]
			start := time.Now()
			got, err := db.GetRequestLog(ctx, r.id)
			r.latencies = append(r.latencies, time.Since(start))
			if err != nil || got.UpstreamResponse != raw {
				b.Fatal("详情未还原完整诊断", err)
			}
		}
	}
	b.StopTimer()
	for _, r := range records {
		slices.Sort(r.latencies)
		p := (len(r.latencies)*95+99)/100 - 1
		b.ReportMetric(float64(r.latencies[p].Microseconds()), r.id+"-p95-us")
		b.ReportMetric(float64(r.stored), r.id+"-stored-B")
	}
}
