package usagespool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// TestLiveStagingReservationAndReplay 暂停真实临时文件的半次写入。在三槽队列中验证其他请求可准入、
// 当前文件的预留不能被抢用、后台恢复不会读取半成品。同步后核对精确金额和重复重放，测试回收私有目录。
func TestLiveStagingReservationAndReplay(t *testing.T) {
	s := openSpool(t, Config{MaxEntries: 3})
	record := spoolRecord("live-stage")
	if err := s.Reserve(record.RequestID); err != nil {
		t.Fatal(err)
	}
	m := commandFor(t, putSnapshot, record)
	peer := commandFor(t, putSnapshot, spoolRecord("live-peer"))
	opened := make(chan error, 1)
	release := make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	finished := make(chan error, 1)
	go func() {
		s.mu.Lock()
		s.applyBatchLocked([]*mutation{m, peer}, func(name string, raw []byte) error {
			if bytes.Equal(raw, peer.raw) {
				return s.stageFile(name, raw)
			}
			f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				opened <- err
				return err
			}
			_, err = f.Write(raw[:1])
			opened <- err
			if err == nil {
				<-release
				_, err = f.Write(raw[1:])
			}
			return errors.Join(err, f.Sync(), f.Close())
		}, s.syncDirectoryLocked)
		s.mu.Unlock()
		finished <- errors.Join(m.err, peer.err)
	}()
	select {
	case err := <-opened:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未进入锁外暂存")
	}
	if !s.mu.TryLock() {
		t.Fatal("临时文件写入期间仍持有目录锁")
	}
	s.mu.Unlock()
	s.Release(record.RequestID)
	if err := s.ReserveForAttempt("other"); err != nil {
		t.Fatalf("无关请求无法使用剩余槽: %v", err)
	}
	if err := s.ReserveForAttempt("excess"); !errors.Is(err, ErrFull) {
		t.Fatalf("在途账单的容量被抢用: %v", err)
	}
	st, err := s.Status()
	if err != nil || st.Replayable != 0 || st.Pending != 0 || st.Reserved != 3 || st.Quarantined != 0 {
		t.Fatalf("半成品进入可重放记录或未预留容量: %+v / %v", st, err)
	}
	count, err := s.ReplayForAdmission(t.Context(), 5, func(context.Context, iam.UsageRecord) error { return errors.New("读到尚未写完的账单") })
	if err != nil || count != 0 {
		t.Fatalf("恢复者读取了半成品: %d / %v", count, err)
	}
	resume()
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	s.Release("other")
	seen := map[string]bool{}
	for i, want := range []int{2, 0} {
		n, err := s.Replay(t.Context(), 5, func(_ context.Context, got iam.UsageRecord) error {
			if (got.RequestID != record.RequestID && got.RequestID != peer.record.RequestID) || seen[got.RequestID] || got.Cost != record.Cost {
				return errors.New("暂存后请求号或金额变化")
			}
			seen[got.RequestID] = true
			return nil
		})
		if err != nil || n != want {
			t.Fatalf("第%d轮重放=%d, 期望%d: %v", i+1, n, want, err)
		}
	}
}

// TestStagingRechecksChangedDirectory 在第一次规划后注入真实目录变化，验证回锁检查会保留同名冲突、
// 消除完全相同的副本并拒绝超额发布。所有原文件、暂存和隔离证据都位于会自动销毁的目录。
func TestStagingRechecksChangedDirectory(t *testing.T) {
	for _, mode := range []string{"conflict", "identical", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			s := openSpool(t, Config{MaxEntries: 2})
			m := commandFor(t, putSnapshot, spoolRecord("revalidate"))
			other := m.record
			if mode == "capacity" {
				other.RequestID = "another"
			}
			if mode == "conflict" {
				other.Cost++
			}
			raw, err := encode(other)
			if err != nil {
				t.Fatal(err)
			}
			peer := commandFor(t, putSnapshot, spoolRecord("revalidate-peer"))
			s.mu.Lock()
			s.applyBatchLocked([]*mutation{peer, m}, func(name string, data []byte) error {
				if err := s.stageFile(name, data); err != nil {
					return err
				}
				if bytes.Equal(data, peer.raw) {
					return nil
				}
				return os.WriteFile(filepath.Join(s.config.Dir, basename(other.RequestID)), raw, 0600)
			}, s.syncDirectoryLocked)
			s.mu.Unlock()
			if peer.err != nil {
				t.Fatalf("同批独立账单发布失败: %v", peer.err)
			}
			st, e := s.Status()
			if e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "identical":
				if m.err != nil || st.Pending != 2 || st.Quarantined != 0 {
					t.Fatalf("相同快照未去重: %v %+v", m.err, st)
				}
			case "conflict":
				if !errors.Is(m.err, ErrConflict) || st.Quarantined != 1 {
					t.Fatalf("冲突被覆盖或证据缺失: %v %+v", m.err, st)
				}
			case "capacity":
				if !errors.Is(m.err, ErrFull) || st.Pending != 2 || st.Quarantined != 1 {
					t.Fatalf("回锁后容量或证据错误: %v %+v", m.err, st)
				}
			}
			got, e := os.ReadFile(filepath.Join(s.config.Dir, basename(other.RequestID)))
			if e != nil || !bytes.Equal(raw, got) {
				t.Fatalf("已存在快照被覆盖: %v", e)
			}
		})
	}
}

// TestCrashDuringStagedFinal 在子进程把有费用终态文件实际 fsync、尚未发布时硬退出。父进程验证旧零费
// 标记仍在，重启恢复须优先采用完整终态及同批另一笔，二次重放不再扣款。测试只使用临时目录并检查子进程退出码。
func TestCrashDuringStagedFinal(t *testing.T) {
	const key = "XHUB_UNIT_STAGED_CRASH_DIR"
	dir := os.Getenv(key)
	if dir != "" {
		s := openSpool(t, Config{Dir: dir})
		marker, final := deliveryPair(t, "process-stage-final")
		if err := s.Reserve(marker.RequestID); err != nil {
			t.Fatal(err)
		}
		if err := s.BeginDelivery(marker); err != nil {
			t.Fatal(err)
		}
		m := commandFor(t, completeMarker, final)
		peer := commandFor(t, putSnapshot, spoolRecord("crash-peer"))
		peerSynced := make(chan struct{})
		s.mu.Lock()
		s.applyBatchLocked([]*mutation{m, peer}, func(name string, raw []byte) error {
			if err := s.stageFile(name, raw); err != nil {
				return err
			}
			if bytes.Equal(raw, peer.raw) {
				close(peerSynced)
				return nil
			}
			<-peerSynced
			os.Exit(42)
			return nil
		}, s.syncDirectoryLocked)
		s.mu.Unlock()
		t.Fatalf("没有在暂存持久化边界退出: %v", m.err)
	}
	dir = t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCrashDuringStagedFinal$")
	cmd.Env = append(os.Environ(), key+"="+dir)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 {
		t.Fatalf("子进程未在目标检查点退出: %v %s", err, output)
	}
	marker, final := deliveryPair(t, "process-stage-final")
	initial, e := os.ReadFile(filepath.Join(dir, basename(marker.RequestID)))
	if e != nil {
		t.Fatal(e)
	}
	old, e := decode(initial, basename(marker.RequestID))
	if e != nil || !old.DeliveryUnconfirmed || old.Cost != 0 {
		t.Fatalf("旧标记被提前改写: %+v %v", old, e)
	}
	files, e := filepath.Glob(filepath.Join(dir, ".pending-*.tmp"))
	if e != nil || len(files) != 2 {
		t.Fatalf("恢复前完整临时终态数=%d: %v", len(files), e)
	}
	recovered := openSpool(t, Config{Dir: dir})
	expected := map[string]float64{final.RequestID: final.Cost, "crash-peer": spoolRecord("crash-peer").Cost}
	for _, want := range []int{2, 0} {
		n, err := recovered.Replay(t.Context(), 16, func(_ context.Context, r iam.UsageRecord) error {
			cost, ok := expected[r.RequestID]
			if !ok || r.DeliveryUnconfirmed || r.Cost != cost {
				return errors.New("完整有费终态被旧标记覆盖")
			}
			delete(expected, r.RequestID)
			return nil
		})
		if err != nil || n != want {
			t.Fatalf("崩溃重放应入账%d笔，实际%d: %v", want, n, err)
		}
	}
}

// TestStageFileFailureRecovery 在专用目录中分别产生半写 ENOSPC 和完整写入后的真实 Sync 错误。
// 同一组的另一笔必须照常入账。重新打开后半写证据隔离，完整证据至多入账一次；框架回收所有文件。
func TestStageFileFailureRecovery(t *testing.T) {
	for _, partial := range []bool{true, false} {
		label := "sync-after-close"
		if partial {
			label = "partial-write"
		}
		t.Run(label, func(t *testing.T) {
			s := openSpool(t, Config{})
			bad := commandFor(t, putSnapshot, spoolRecord("failed-stage"))
			good := commandFor(t, putSnapshot, spoolRecord("durable-sibling"))
			s.mu.Lock()
			s.applyBatchLocked([]*mutation{bad, good}, func(name string, raw []byte) error {
				if !bytes.Equal(raw, bad.raw) {
					return s.stageFile(name, raw)
				}
				f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_EXCL|os.O_CREATE, 0600)
				if err != nil {
					return err
				}
				if partial {
					_, err = f.Write(raw[:len(raw)/2])
					return errors.Join(err, f.Close(), syscall.ENOSPC)
				}
				_, err = f.Write(raw)
				return errors.Join(err, f.Close(), f.Sync())
			}, s.syncDirectoryLocked)
			s.mu.Unlock()
			wantErr := error(os.ErrClosed)
			if partial {
				wantErr = syscall.ENOSPC
			}
			if !errors.Is(bad.err, wantErr) || good.err != nil {
				t.Fatalf("故障账单未报告原始错误或同批正常账单受影响: %v / %v", bad.err, good.err)
			}
			if _, err := s.root.Lstat(bad.name); !os.IsNotExist(err) {
				t.Fatalf("写入失败却发布了最终账单: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			recovered := openSpool(t, Config{Dir: s.config.Dir})
			state, err := recovered.Status()
			if err != nil || state.Quarantined != boolInt(partial) {
				t.Fatalf("故障证据隔离情况异常: %+v / %v", state, err)
			}
			seen := map[string]int{}
			for i, want := range []int{2 - boolInt(partial), 0} {
				n, err := recovered.Replay(t.Context(), 10, func(_ context.Context, r iam.UsageRecord) error {
					seen[r.RequestID]++
					if r.Cost != good.record.Cost {
						return errors.New("重放费用发生变化")
					}
					return nil
				})
				if err != nil || n != want {
					t.Fatalf("第 %d 次故障恢复入账 %d 笔，期望 %d: %v", i+1, n, want, err)
				}
			}
			if seen[good.record.RequestID] != 1 || seen[bad.record.RequestID] != boolInt(!partial) {
				t.Fatalf("故障恢复漏记或重复计费: %v", seen)
			}
		})
	}
}

// TestCloseProtectsLiveStaging 验证多笔账单在锁外写盘时仍持有进程级目录锁。
// 并行关闭必须等已接受的账单发布后才允许新实例接管；等待结束后从私有目录重放并由框架清理。
func TestCloseProtectsLiveStaging(t *testing.T) {
	s := openSpool(t, Config{GroupSize: 2})
	m := commandFor(t, putSnapshot, spoolRecord("closing-stage"))
	peer := commandFor(t, putSnapshot, spoolRecord("close-peer"))
	started, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	written := make(chan error, 1)
	go func() {
		s.mu.Lock()
		s.applyBatchLocked([]*mutation{m, peer}, func(name string, raw []byte) error {
			if bytes.Equal(raw, peer.raw) {
				return s.stageFile(name, raw)
			}
			close(started)
			<-release
			return s.stageFile(name, raw)
		}, s.syncDirectoryLocked)
		s.mu.Unlock()
		written <- errors.Join(m.err, peer.err)
	}()
	<-started
	closing := make(chan error, 1)
	go func() { closing <- s.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for s.probeMu.TryLock() {
		s.probeMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("关闭操作未在暂存期间进入等待")
		}
		time.Sleep(time.Millisecond)
	}
	if second, err := Open(Config{Dir: s.config.Dir}); err == nil {
		_ = second.Close()
		t.Fatal("锁外写入尚未结束时第二个实例取得了队列")
	}
	resume()
	if err := errors.Join(<-written, <-closing); err != nil {
		t.Fatal(err)
	}
	second := openSpool(t, Config{Dir: s.config.Dir})
	seen := map[string]bool{}
	n, err := second.Replay(t.Context(), 2, func(_ context.Context, r iam.UsageRecord) error {
		if (r.RequestID != m.record.RequestID && r.RequestID != peer.record.RequestID) || seen[r.RequestID] || r.Cost != m.record.Cost {
			return errors.New("关闭后恢复了错误的账单")
		}
		seen[r.RequestID] = true
		return nil
	})
	if n != 2 || err != nil {
		t.Fatalf("接管后应收到两笔已确认账单: n=%d %v", n, err)
	}
}

// TestDiscardDuringDeliveryStaging 在真实零费标记上暂停终态写入，同时发起已入库账单的清理。
// 其他请求的清理可以完成；同一请求须保存终态并同步后才可核销。测试结束没有待扣款的文件。
func TestDiscardDuringDeliveryStaging(t *testing.T) {
	s := openSpool(t, Config{})
	marker, final := deliveryPair(t, "discard-overlap")
	if err := s.Reserve(marker.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	m := commandFor(t, completeMarker, final)
	peer := commandFor(t, putSnapshot, spoolRecord("discard-peer"))
	started, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	defer resume()
	written, discarded := make(chan error, 1), make(chan error, 1)
	go func() {
		s.mu.Lock()
		s.applyBatchLocked([]*mutation{m, peer}, func(name string, raw []byte) error {
			if bytes.Equal(raw, peer.raw) {
				return s.stageFile(name, raw)
			}
			err := s.stageFile(name, raw)
			close(started)
			<-release
			return err
		}, s.syncDirectoryLocked)
		s.mu.Unlock()
		written <- errors.Join(m.err, peer.err)
	}()
	<-started
	other := final
	other.RequestID = "unrelated-discard"
	if err := s.DiscardUnconfirmedDelivery(other); err != nil {
		t.Fatalf("无关账单的清理被暂存写入阻断: %v", err)
	}
	entered := make(chan struct{})
	go func() {
		close(entered)
		discarded <- s.DiscardUnconfirmedDelivery(final)
	}()
	<-entered
	select {
	case err := <-discarded:
		t.Fatalf("写盘完成前删除了同一请求的交付凭据: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	resume()
	if err := errors.Join(<-written, <-discarded); err != nil {
		t.Fatal(err)
	}
	if err := s.Acknowledge(peer.record); err != nil {
		t.Fatalf("同批账单未发布: %v", err)
	}
	status, err := s.Status()
	if err != nil || status.Pending != 0 || status.Reserved != 0 || !status.Healthy {
		t.Fatalf("同步完成后仍有需要重扣的账单或占用的预留: %+v %v", status, err)
	}
}

// TestUnlockOnlyForMultipleNewFiles 预置一笔已入账待核销文件和一笔可幂等重交的账单，混入零、一或两笔新写入。
// 文件写入钩子检查目录锁边界，发布屏障始终持锁，最终逐笔核对幂等、正常结果与待处理数量。私有目录由 t 清理。
func TestUnlockOnlyForMultipleNewFiles(t *testing.T) {
	for _, fresh := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("new=%d", fresh), func(t *testing.T) {
			s := openSpool(t, Config{})
			acked, existing := spoolRecord("already-charged"), spoolRecord("already-stored")
			if err := errors.Join(s.Put(acked), s.Put(existing)); err != nil {
				t.Fatal(err)
			}
			batch := []*mutation{commandFor(t, acknowledgeSnapshot, acked), commandFor(t, putSnapshot, existing)}
			for i := 0; i < fresh; i++ {
				batch = append(batch, commandFor(t, putSnapshot, spoolRecord(fmt.Sprintf("new-%d", i))))
			}
			var staged atomic.Int32
			var firstUnlocked, barrierUnlocked bool
			s.mu.Lock()
			s.applyBatchLocked(batch, func(name string, raw []byte) error {
				staged.Add(1)
				if fresh > 0 && bytes.Equal(raw, batch[2].raw) && s.mu.TryLock() {
					firstUnlocked = true
					s.mu.Unlock()
				}
				return s.stageFile(name, raw)
			}, func(dir string) error {
				if s.mu.TryLock() {
					barrierUnlocked = true
					s.mu.Unlock()
				}
				return s.syncDirectoryLocked(dir)
			})
			s.mu.Unlock()
			for _, m := range batch {
				if m.err != nil {
					t.Fatalf("操作 %s 未正确完成: %v", m.record.RequestID, m.err)
				}
			}
			state, err := s.Status()
			if int(staged.Load()) != fresh || firstUnlocked != (fresh >= 2) || barrierUnlocked || err != nil || state.Pending != 1+fresh || state.Quarantined != 0 {
				t.Fatalf("实际文件数或锁边界不符: 新文件=%d 写入=%d 写入解锁=%v 目录同步解锁=%v 队列=%+v/%v", fresh, staged.Load(), firstUnlocked, barrierUnlocked, state, err)
			}
		})
	}
}

// TestStagedBatchRechecksDeliveryOwner 在独立目录预置一条交付标记，让同批的有费终态与普通账单完成文件同步。
// 发布前替换标记归属，验证原件保留、冲突终态带原金额隔离、同批另一笔能正常发布。测试目录由 t 回收。
func TestStagedBatchRechecksDeliveryOwner(t *testing.T) {
	s := openSpool(t, Config{})
	marker, final := deliveryPair(t, "reassigned-marker")
	if err := s.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	changed := marker
	changed.UserID = "other-test-owner"
	replacement, err := encode(changed)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := commandFor(t, completeMarker, final)
	peer := commandFor(t, putSnapshot, spoolRecord("independent-bill"))
	s.mu.Lock()
	s.applyBatchLocked([]*mutation{confirmed, peer}, func(name string, raw []byte) error {
		if err := s.stageFile(name, raw); err != nil {
			return err
		}
		if bytes.Equal(raw, confirmed.raw) {
			return os.WriteFile(filepath.Join(s.config.Dir, confirmed.name), replacement, 0600)
		}
		return nil
	}, s.syncDirectoryLocked)
	s.mu.Unlock()
	actualMarker, err := os.ReadFile(filepath.Join(s.config.Dir, confirmed.name))
	if !errors.Is(confirmed.err, ErrConflict) || err != nil || !bytes.Equal(actualMarker, replacement) {
		t.Fatalf("发生归属冲突后覆盖了已有交付凭据: 发布=%v 读取=%v", confirmed.err, err)
	}
	evidence, err := filepath.Glob(filepath.Join(s.config.Dir, "quarantine", "*.evidence"))
	if err != nil || len(evidence) != 1 {
		t.Fatalf("有费终态的隔离证据数=%d: %v", len(evidence), err)
	}
	raw, err := os.ReadFile(evidence[0])
	if err != nil || !bytes.Equal(raw, confirmed.raw) {
		t.Fatalf("隔离的终态内容或金额改变: %v", err)
	}
	actualPeer, err := os.ReadFile(filepath.Join(s.config.Dir, peer.name))
	if peer.err != nil || err != nil || !bytes.Equal(actualPeer, peer.raw) {
		t.Fatalf("同批独立账单发布失败或内容错误: 发布=%v 读取=%v", peer.err, err)
	}
}
