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
			s.replaying = &replayBatch{pending: map[string]bool{ack.name: true}}
			injected := errors.New("isolated sync failure")
			started, release := make(chan bool, len(batch)), make(chan struct{})
			entered, finish := make(chan struct{}), make(chan struct{})
			done := make(chan struct{})
			var stages, barriers atomic.Int32
			go func() {
				s.applyBatchLocked(batch, func(name string, raw []byte) error {
					stages.Add(1)
					started <- true
					<-release
					if failure == "one-stage" && bytes.Equal(batch[0].raw, raw) {
						return injected
					}
					err := s.stageFile(name, raw)
					if failure == "one-rename" && bytes.Equal(batch[0].raw, raw) && err == nil {
						return s.root.Rename(name, ".pending-missing-fixture.tmp")
					}
					return err
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

// TestGroupPublicQueueAndSerialFallback 锁住目录让并发请求真实排队，然后通过目录屏障计数验证真实合并、幂等重试和容量回退。
// 同一 ID 顺序执行，容量临界使用原逐笔准入；同时验证关闭/非法批量大小及已发布文件数量，临时目录自动回收。
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
			// 先拦住默认启动器，再用同一生产 drain 循环记录它实际执行的目录屏障。
			s.writes.mu.Lock()
			s.writes.running = true
			s.writes.mu.Unlock()
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
			var barriers atomic.Int32
			go s.drainMutations(s.stageFile, func(dir string) error {
				barriers.Add(1)
				return s.syncDirectoryLocked(dir)
			})
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
			if mode == "normal" && barriers.Load() != 1 {
				t.Errorf("四次排队 Put 应共享一次真实目录屏障，实际=%d", barriers.Load())
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

// TestGroupSaturatedOwnReservations 在真实目录中占满 64 份字节和条数预留，再提交 16 笔独立终态。
// 验证仅使用自己的槽即可共享一次目录同步，额外的未预留写不能占用别人或越过条数上限；目录自动回收。
func TestGroupSaturatedOwnReservations(t *testing.T) {
	const slots, recordBytes = 64, 4096
	s := openSpool(t, Config{MaxEntries: slots, MaxBytes: slots * recordBytes, MaxRecordBytes: recordBytes})
	var batch []*mutation
	for i := range slots {
		id := fmt.Sprintf("saturated-%02d", i)
		if err := s.Reserve(id); err != nil {
			t.Fatalf("预留第 %d 个独立槽失败: %v", i, err)
		}
		if i < DefaultGroupSize {
			batch = append(batch, commandFor(t, putSnapshot, spoolRecord(id)))
		}
	}
	if err := s.Reserve("excess"); !errors.Is(err, ErrFull) {
		t.Fatalf("容量已满的额外调用未拒绝: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	barriers := 0
	s.applyBatchLocked(batch, s.stageFile, func(dir string) error {
		barriers++
		return s.syncDirectoryLocked(dir)
	})
	for _, m := range batch {
		current, err := s.readLocked(m.name)
		if m.err != nil || err != nil || !bytes.Equal(current, m.raw) || s.reserved[m.record.RequestID] {
			t.Fatalf("满预留组未逐笔持久化或释放归属: %v/%v", m.err, err)
		}
	}
	if barriers != 1 || len(s.reserved) != slots-len(batch) {
		t.Fatalf("满负载写入未共享屏障或借用了其他预留: 屏障=%d 剩余预留=%d", barriers, len(s.reserved))
	}
	excess := commandFor(t, putSnapshot, spoolRecord("excess"))
	s.applySingleLocked(excess, s.stageFile, s.syncDirectoryLocked)
	if err := excess.err; !errors.Is(err, ErrFull) {
		t.Fatalf("饱和条数下未预留记录越过上限: %v", err)
	}
}

// TestGroupIsolatesFallbackAndIdempotency 在私有目录中交错首次写入、重复提交、已删除 ACK 及真正的内容冲突。
// 验证屏障批次、原证据字节、隔离和逐笔错误；所有快照和隔离文件由临时目录回收。
func TestGroupIsolatesFallbackAndIdempotency(t *testing.T) {
	s := openSpool(t, Config{})
	original := spoolRecord("original")
	if err := s.Put(original); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := commandFor(t, putSnapshot, original)
	conflict := original
	conflict.Cost++
	missingAck := commandFor(t, acknowledgeSnapshot, spoolRecord("already-removed"))
	badAck := commandFor(t, acknowledgeSnapshot, conflict)
	mutations := []*mutation{
		commandFor(t, putSnapshot, spoolRecord("first")), old, missingAck, commandFor(t, putSnapshot, spoolRecord("second")),
		badAck, commandFor(t, putSnapshot, spoolRecord("third")), commandFor(t, putSnapshot, spoolRecord("fourth")),
	}
	var stages atomic.Int32
	barriers := 0
	s.applyBatchLocked(mutations, func(name string, raw []byte) error {
		stages.Add(1)
		return s.stageFile(name, raw)
	}, func(dir string) error {
		barriers++
		return s.syncDirectoryLocked(dir)
	})
	for _, m := range mutations {
		if (m != badAck && m.err != nil) || (m == badAck && !errors.Is(m.err, ErrConflict)) {
			t.Fatalf("无关错误传给其他账单或错放 ACK: %v", m.err)
		}
	}
	if stages.Load() != 4 || barriers != 2 || s.generation != 5 {
		t.Fatalf("回退连累独立写入或冗余写盘: staging=%d barriers=%d generation=%d", stages.Load(), barriers, s.generation)
	}
	group := []*mutation{commandFor(t, putSnapshot, original), commandFor(t, putSnapshot, conflict)}
	s.applyBatchLocked(group, s.stageFile, s.syncDirectoryLocked)
	current, err := s.readLocked(old.name)
	state, statErr := s.statusLocked()
	if group[0].err != nil || !errors.Is(group[1].err, ErrConflict) || err != nil || !bytes.Equal(current, old.raw) || statErr != nil || state.Quarantined != 1 {
		t.Fatalf("相同ID冲突未保留原文件和单独的隔离证据: %v/%v 读取=%v 状态=%+v/%v", group[0].err, group[1].err, err, state, statErr)
	}
}

// TestGroupFailureStateAndHealth 以真实旧标记、预留、已入账文件构造混合操作，分别注入暂存、删除或目录同步失败。
// 验证每种操作的活动所有权、预留、代际和健康度与逐笔写入一致；测试结束自动删除全部数据。
func TestGroupFailureStateAndHealth(t *testing.T) {
	for _, mode := range []string{"put-first", "put-last", "begin", "complete", "ack-remove", "ack-directory", "ack-only"} {
		t.Run(mode, func(t *testing.T) {
			s := openSpool(t, Config{})
			marker, final := deliveryPair(t, "delivery")
			plain, acked := spoolRecord("plain"), spoolRecord("stored")
			if err := s.Reserve(marker.RequestID); err != nil {
				t.Fatal(err)
			}
			if err := s.Put(acked); err != nil {
				t.Fatal(err)
			}
			if mode == "complete" {
				if err := s.BeginDelivery(marker); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			injected := errors.New("durability fixture")
			s.lastErr = ErrFull
			ack := commandFor(t, acknowledgeSnapshot, acked)
			s.replaying = &replayBatch{pending: map[string]bool{ack.name: true}}
			write := commandFor(t, putSnapshot, plain)
			var target *mutation
			commands := []*mutation{write, ack}
			wantHealth := error(nil)
			switch mode {
			case "put-first", "put-last":
				target = commandFor(t, putSnapshot, spoolRecord("failed"))
				if mode == "put-first" {
					commands = []*mutation{target, write, ack}
				} else {
					commands = []*mutation{write, target, ack}
					wantHealth = injected
				}
			case "begin":
				target = commandFor(t, beginMarker, marker)
				commands, wantHealth = []*mutation{target, ack}, injected
			case "complete":
				target = commandFor(t, completeMarker, final)
				commands, wantHealth = []*mutation{target, ack}, injected
			case "ack-directory", "ack-only":
				commands = []*mutation{commandFor(t, acknowledgeSnapshot, spoolRecord("previously removed")), ack}
				wantHealth = ErrFull
			}
			before := s.generation
			s.applyBatchLocked(commands, func(name string, raw []byte) error {
				if target != nil && bytes.Equal(raw, target.raw) {
					return injected
				}
				if mode == "ack-remove" {
					if err := s.root.Remove(ack.name); err != nil {
						return err
					}
				}
				return s.stageFile(name, raw)
			}, func(dir string) error {
				if mode == "ack-directory" {
					return injected
				}
				return s.syncDirectoryLocked(dir)
			})
			ackFailed := mode == "ack-directory" || mode == "ack-remove"
			if (ack.err != nil) != ackFailed || s.replaying.pending[ack.name] != ackFailed || !errors.Is(s.lastErr, wantHealth) {
				t.Fatalf("ACK水位或写盘健康度改变: ack=%v pending=%v 健康=%v 期望=%v", ack.err, s.replaying.pending[ack.name], s.lastErr, wantHealth)
			}
			if target != nil && !errors.Is(target.err, injected) {
				t.Fatalf("目标写入未报告实际暂存错误: %v", target.err)
			}
			if s.reserved[marker.RequestID] != (mode != "begin") || s.activeDelivery[basename(marker.RequestID)] != (mode == "complete") {
				t.Fatalf("失败后预留或在途归属错误: reserved=%v active=%v", s.reserved, s.activeDelivery)
			}
			var writes uint64
			for _, op := range commands {
				if op.kind != acknowledgeSnapshot {
					writes++
				}
			}
			if s.generation != before+writes {
				t.Fatalf("代际增量=%d，写入尝试=%d", s.generation-before, writes)
			}
		})
	}
}

// TestGroupCompletionCapacity 在满额预留下构造四个属于本实例的在途标记，再合并替换为已确认终态。
// 逐一核实费用快照、单次目录同步和预留归还，并检查越过字节上限的未预留终态不能借用别人的预留。目录自动清理。
func TestGroupCompletionCapacity(t *testing.T) {
	const recordBytes = 65536
	s := openSpool(t, Config{MaxEntries: 16, MaxBytes: 8 * recordBytes, MaxRecordBytes: recordBytes})
	for i := range 8 {
		if err := s.Reserve(fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	var batch []*mutation
	for i := range 4 {
		marker, final := deliveryPair(t, fmt.Sprint(i))
		if err := s.BeginDelivery(marker); err != nil {
			t.Fatal(err)
		}
		batch = append(batch, commandFor(t, completeMarker, final))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := batch[0]
	space := groupSpace{bytes: -recordBytes, entries: 1}
	delete(s.reserved, m.record.RequestID)
	if s.planMutationLocked(m, &space) {
		t.Fatal("未预留终态在字节不足时借用了其他调用的槽")
	}
	s.reserved[m.record.RequestID] = true
	barriers := 0
	s.applyBatchLocked(batch, s.stageFile, func(dir string) error {
		barriers++
		return s.syncDirectoryLocked(dir)
	})
	for _, m := range batch {
		raw, err := s.readLocked(m.name)
		if m.err != nil || err != nil || !bytes.Equal(raw, m.raw) || s.reserved[m.record.RequestID] || s.activeDelivery[m.name] {
			t.Fatalf("预留终态未正确覆盖标记并归还本笔所有权: %v/%v", m.err, err)
		}
	}
	if barriers != 1 || len(s.reserved) != 4 {
		t.Fatalf("饱和终态发生冗余同步或释放其他预留: 目录同步=%d 剩余预留=%d", barriers, len(s.reserved))
	}
}
