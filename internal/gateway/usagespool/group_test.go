package usagespool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// deliveryPair 构建同一请求的真实零费在途标记及完整终态；用于核对跨阶段归属和恢复语义，数据仅在夹具内。
func deliveryPair(t *testing.T, id string) (iam.UsageRecord, iam.UsageRecord) {
	t.Helper()
	final := spoolRecord(id)
	final.DeliveryPending, final.DeliverySpoolOnly, final.Status, final.HTTPStatus = true, true, "success", 200
	marker, err := iam.UnconfirmedDelivery(final)
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	return marker, final
}

// commandFor 以产品序列化器建立一笔确定类型的测试命令；只编码合成快照，失败直接报告测试。
func commandFor(t *testing.T, kind mutationKind, r iam.UsageRecord) *mutation {
	t.Helper()
	raw, err := encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return &mutation{kind: kind, record: r, name: basename(r.RequestID), raw: raw}
}

// TestGroupDurableStagesAndFailureIsolation 用真实临时目录控制三份独立快照的文件同步及共享目录屏障。
// 核实同步前不改名、屏障前不释放归属/重放水位、单笔失败与整组目录失败分别传播，并重开 v1 文件恢复；文件自动清理。
func TestGroupDurableStagesAndFailureIsolation(t *testing.T) {
	for _, failure := range []string{"none", "one-stage", "one-rename", "directory"} {
		t.Run(failure, func(t *testing.T) {
			s := openSpool(t, Config{})
			marker, final := deliveryPair(t, "complete")
			newMarker, _ := deliveryPair(t, "begin")
			plain, settled := spoolRecord("plain"), spoolRecord("acked")
			for _, id := range []string{marker.RequestID, newMarker.RequestID, plain.RequestID} {
				if err := s.Reserve(id); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.BeginDelivery(marker); err != nil {
				t.Fatal(err)
			}
			if err := s.Put(settled); err != nil {
				t.Fatal(err)
			}
			batch := []*mutation{commandFor(t, putSnapshot, plain), commandFor(t, beginMarker, newMarker), commandFor(t, completeMarker, final), commandFor(t, acknowledgeSnapshot, settled)}
			ack := batch[3]
			s.mu.Lock()
			if !s.canGroupLocked(batch) {
				s.mu.Unlock()
				t.Fatal("独立正常请求未获合并资格")
			}
			s.replaying = &replayBatch{pending: map[string]bool{ack.name: true}}
			injected := errors.New("isolated sync failure")
			started, release := make(chan bool, len(batch)), make(chan struct{})
			entered, finish := make(chan struct{}), make(chan struct{})
			done := make(chan struct{})
			var stages, barriers atomic.Int32
			go func() {
				s.applyGroupLocked(batch, func(raw []byte) (string, error) {
					stages.Add(1)
					started <- true
					<-release
					if failure == "one-stage" && bytes.Equal(batch[0].raw, raw) {
						return "", injected
					}
					tmp, err := s.stageFile(raw)
					if failure == "one-rename" && bytes.Equal(batch[0].raw, raw) && err == nil {
						return ".missing-fixture", nil
					}
					return tmp, err
				}, func(name string) error {
					barriers.Add(1)
					close(entered)
					<-finish
					if failure == "directory" {
						return injected
					}
					return s.syncDirectoryLocked(name)
				})
				close(done)
			}()
			for range 3 {
				<-started
			}
			// 三个 fsync 均已进入但均未返回：旧标记必须仍完整，新文件不可见。
			for _, r := range []iam.UsageRecord{plain, newMarker} {
				if _, err := s.root.Stat(basename(r.RequestID)); !os.IsNotExist(err) {
					t.Errorf("文件同步完成前发布了 %s: %v", r.RequestID, err)
				}
			}
			if !s.activeDelivery[batch[2].name] || !s.replaying.pending[ack.name] {
				t.Error("写盘开始前水位异常")
			}
			close(release)
			<-entered
			if !s.activeDelivery[batch[2].name] || !s.reserved[final.RequestID] || s.activeDelivery[batch[1].name] || !s.replaying.pending[ack.name] {
				t.Error("目录同步成功前提前改变内存状态")
			}
			close(finish)
			<-done
			if stages.Load() != 3 || barriers.Load() != 1 {
				t.Errorf("共享屏障次数错误: 文件=%d 目录=%d", stages.Load(), barriers.Load())
			}
			for i, m := range batch {
				wantErr := failure == "directory" || i == 0 && (failure == "one-stage" || failure == "one-rename")
				if (m.err != nil) != wantErr {
					t.Errorf("操作%d错误传播错误: %v", i, m.err)
				}
			}
			if failure == "directory" {
				if !s.activeDelivery[batch[2].name] || !s.reserved[final.RequestID] || s.activeDelivery[batch[1].name] || !s.replaying.pending[ack.name] {
					t.Error("同步失败却发布成功或移除恢复水位")
				}
			} else if s.activeDelivery[batch[2].name] || s.reserved[final.RequestID] || !s.activeDelivery[batch[1].name] || s.replaying.pending[ack.name] {
				t.Error("已同步操作没有逐笔发布")
			}
			s.replaying = nil
			s.mu.Unlock()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openSpool(t, Config{Dir: s.config.Dir, GroupSize: 1})
			var recovered []string
			_, err := reopened.Replay(t.Context(), 16, func(_ context.Context, r iam.UsageRecord) error {
				switch r.RequestID {
				case final.RequestID:
					if r.DeliveryUnconfirmed || r.Cost != final.Cost {
						return fmt.Errorf("确认终态未正确恢复: %v", r.Cost)
					}
				case newMarker.RequestID:
					if !r.DeliveryUnconfirmed || r.Cost != 0 {
						return fmt.Errorf("在途标记被错误收费")
					}
				case plain.RequestID:
					if r.Cost != plain.Cost {
						return fmt.Errorf("普通账单费用错误")
					}
				default:
					return fmt.Errorf("已确认删除的账单重新出现: %s", r.RequestID)
				}
				recovered = append(recovered, r.RequestID)
				return nil
			})
			want := 3
			if failure == "one-stage" {
				want = 2
			}
			if err != nil || len(recovered) != want {
				t.Errorf("旧格式恢复缺失或多记: recovered=%v err=%v", recovered, err)
			}
		})
	}
}

// TestGroupPublicQueueAndSerialFallback 锁住目录让并发请求真实排队，然后验证去重冲突、条数极限及正常写入。
// 同一 ID 顺序执行，容量临界使用原逐笔准入；同时验证关闭/非法批量大小及已有文件完整性，临时目录自动回收。
func TestGroupPublicQueueAndSerialFallback(t *testing.T) {
	for _, mode := range []string{"normal", "same", "capacity", "closed"} {
		t.Run(mode, func(t *testing.T) {
			cfg := Config{GroupSize: 4}
			if mode == "capacity" {
				cfg.MaxEntries = 1
			}
			s := openSpool(t, cfg)
			if mode == "closed" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			results := make(chan error, 4)
			for i := range 4 {
				id := fmt.Sprintf("r%d", i)
				if mode == "same" {
					id = "same"
				}
				go func() { results <- s.Put(spoolRecord(id)) }()
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				s.writes.mu.Lock()
				queued := len(s.writes.waiting)
				s.writes.mu.Unlock()
				if queued == 4 {
					break
				}
				if time.Now().After(deadline) {
					s.mu.Unlock()
					t.Fatalf("实际提交者未按预期排队: %d", queued)
				}
				runtime.Gosched()
			}
			s.mu.Unlock()
			success := 0
			for range 4 {
				err := <-results
				switch {
				case err == nil:
					success++
				case mode == "capacity" && errors.Is(err, ErrFull), mode == "closed" && errors.Is(err, ErrUnavailable):
				default:
					t.Errorf("未预期的逐笔错误: %v", err)
				}
			}
			if mode == "closed" {
				if success != 0 {
					t.Fatal("关闭后仍新增快照")
				}
				return
			}
			want := 4
			if mode == "capacity" {
				want = 1
			}
			if success != want {
				t.Errorf("容量或幂等行为变化: success=%d want=%d", success, want)
			}
			if mode == "same" {
				want = 1
			}
			files, err := filepath.Glob(filepath.Join(s.config.Dir, "*.json"))
			if err != nil || len(files) != want {
				t.Errorf("已发布文件条数错误: got=%d want=%d err=%v", len(files), want, err)
			}
		})
	}
	for _, invalid := range []int{-1, MaxGroupSize + 1} {
		if _, err := Open(Config{Dir: t.TempDir(), GroupSize: invalid}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("非法批量大小未拒绝: %d err=%v", invalid, err)
		}
	}
}
