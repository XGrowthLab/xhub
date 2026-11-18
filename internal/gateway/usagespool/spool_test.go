package usagespool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunqirui1987/xhub/internal/iam"
)

// spoolRecord 创建带预占/冻结费率/正文的完整用量夹具，参数id唯一标识；不读取真实用户或外部凭据。
func spoolRecord(id string) iam.UsageRecord {
	return iam.UsageRecord{RequestID: id, BudgetReservationID: "hold-" + id, TS: time.Unix(100, 123).UTC(), EndedAt: time.Unix(101, 123).UTC(), Model: "test", UserID: "test-user", Cost: .0123, PromptTokens: 12, CompletionTokens: 3, RequestBody: `{"input":"Synthetic request"}`, ResponseBody: `{"output":"Synthetic reply"}`, PriceSnapshot: `{"frozen":true}`}
}

// openSpool 创建临时独占队列并注册关闭清理；参数t用于失败定位，config覆盖默认目录/容量，不产生持久外部数据。
func openSpool(t *testing.T, config Config) *Spool {
	t.Helper()
	if config.Dir == "" {
		config.Dir = filepath.Join(t.TempDir(), "spool")
	}
	s, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestSpoolRestartAndIdempotence 验证完整快照在关闭重开后恢复，重复Put/Replay不重复提交，权限严格0700/0600。
// 前置临时磁盘，检查reservation/价格/时间/正文逐字段相等及确认后清空；测试结束关闭目录并自动删除。
func TestSpoolRestartAndIdempotence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s := openSpool(t, Config{Dir: dir})
	record := spoolRecord("../not-a-path")
	if err := s.Reserve(record.RequestID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Put(record); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("目录权限不是0700")
	}
	info, err = os.Stat(filepath.Join(dir, basename(record.RequestID)))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("快照权限不是0600")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openSpool(t, Config{Dir: dir})
	committed := 0
	commit := func(_ context.Context, got iam.UsageRecord) error {
		committed++
		if !reflect.DeepEqual(got, record) {
			t.Fatalf("恢复载荷丢字段: %+v", got)
		}
		return nil
	}
	for range 2 {
		if _, err = s.Replay(t.Context(), 100, commit); err != nil {
			t.Fatal(err)
		}
	}
	status, err := s.Status()
	if err != nil || status.Pending != 0 || committed != 1 {
		t.Fatalf("重复恢复或残留: %+v commits=%d err=%v", status, committed, err)
	}
}

// TestSpoolDeliveryMarkerRecovery 验证在途零费用标记不会被后台提前重放，进程退出后可安全恢复。
// 使用独占临时目录，覆盖终态原子替换和未确认退出；测试结束关闭并删除文件。
func TestSpoolDeliveryMarkerRecovery(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirmed=%v", confirmed), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "spool")
			s := openSpool(t, Config{Dir: dir})
			final := spoolRecord("delivery")
			final.Status, final.HTTPStatus = "success", 200
			final.DeliveryPending, final.DeliverySpoolOnly = true, true
			marker, err := iam.UnconfirmedDelivery(final)
			if err != nil {
				t.Fatal(err)
			}
			marker.DeliveryUnconfirmed = true
			if err := s.BeginDelivery(marker); err != nil {
				t.Fatal(err)
			}
			if status, err := s.Status(); err != nil || status.Pending != 1 || status.Replayable != 0 {
				t.Fatalf("在途标记应占容量但不触发准入重放: %+v %v", status, err)
			}
			calls := 0
			if _, err := s.Replay(t.Context(), 10, func(context.Context, iam.UsageRecord) error { calls++; return nil }); err != nil || calls != 0 {
				t.Fatalf("在途标记被提前恢复: calls=%d err=%v", calls, err)
			}
			if confirmed {
				wrong := final
				wrong.UserID = "other-user"
				if err := s.CompleteDelivery(wrong); !errors.Is(err, ErrConflict) {
					t.Fatalf("不同归属不应覆盖标记: %v", err)
				}
				invalid := final
				invalid.Status = "error"
				if err := s.CompleteDelivery(invalid); err == nil {
					t.Fatal("失败终态不应带非零费用")
				}
				if err := s.CompleteDelivery(final); err != nil {
					t.Fatal(err)
				}
				if status, err := s.Status(); err != nil || status.Pending != 1 || status.Replayable != 1 {
					t.Fatalf("最终快照应可重放且继续占容量: %+v %v", status, err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = openSpool(t, Config{Dir: dir})
			if status, err := s.Status(); err != nil || status.Replayable != 1 {
				t.Fatalf("重启后未知标记也应可恢复: %+v %v", status, err)
			}
			if _, err := s.Replay(t.Context(), 10, func(_ context.Context, got iam.UsageRecord) error {
				calls++
				if confirmed && (got.Cost != final.Cost || got.DeliveryUnconfirmed) {
					t.Fatalf("已确认快照错误: %+v", got)
				}
				if !confirmed && (got.Cost != 0 || got.Status != "error" || !got.DeliveryUnconfirmed || got.RequestBody != "" || got.ResponseBody != "") {
					t.Fatalf("未知结果快照错误: %+v", got)
				}
				return nil
			}); err != nil || calls != 1 {
				t.Fatalf("重启恢复失败: calls=%d err=%v", calls, err)
			}
		})
	}
}

// TestSpoolDeliveryInterruptedRename 验证确认终态已经完整写盘但尚未替换标记时，重启能恢复已确认终态。
// 前置独占临时目录，通过完整临时文件模拟 rename 前退出；不访问用户目录并自动清理。
func TestSpoolDeliveryInterruptedRename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	s := openSpool(t, Config{Dir: dir})
	final := spoolRecord("delivery-rename")
	final.Status, final.HTTPStatus = "success", 200
	final.DeliveryPending, final.DeliverySpoolOnly = true, true
	marker, err := iam.UnconfirmedDelivery(final)
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	if err := s.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	raw, err := encode(final)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pending-interrupted.tmp"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openSpool(t, Config{Dir: dir})
	status, err := s.Status()
	if err != nil || status.Quarantined != 0 {
		t.Fatalf("已确认的临时文件被隔离: status=%+v err=%v", status, err)
	}
	count := 0
	_, err = s.Replay(t.Context(), 10, func(_ context.Context, got iam.UsageRecord) error {
		count++
		if got.Cost != final.Cost || got.DeliveryUnconfirmed {
			t.Fatalf("终态恢复错误: %+v", got)
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatalf("未恢复唯一终态: count=%d err=%v", count, err)
	}
}

// TestDiscardUnconfirmedDelivery 验证其他持久队列确认后只能清除同一归属的未确认磁盘标记。
// 使用独占临时目录，覆盖不同归属、重复清理及已有终态，测试结束自动删除目录。
func TestDiscardUnconfirmedDelivery(t *testing.T) {
	s := openSpool(t, Config{})
	final := spoolRecord("delivery-cleanup")
	final.Status, final.HTTPStatus, final.DeliveryPending, final.DeliverySpoolOnly = "success", 200, true, true
	marker, err := iam.UnconfirmedDelivery(final)
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	if err := s.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	wrong := final
	wrong.UserID = "other"
	if err := s.DiscardUnconfirmedDelivery(wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("不能清除其他归属: %v", err)
	}
	if err := s.DiscardUnconfirmedDelivery(final); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardUnconfirmedDelivery(final); err != nil {
		t.Fatal(err)
	}
	if status, err := s.Status(); err != nil || status.Pending != 0 {
		t.Fatalf("标记未清理: %+v %v", status, err)
	}
	final.RequestID = "delivery-cleanup-confirmed"
	marker, err = iam.UnconfirmedDelivery(final)
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	if err := s.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(final); err != nil {
		t.Fatal(err)
	}
	changed := final
	changed.Cost += 1
	if err := s.DiscardUnconfirmedDelivery(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("不同终态不能被确认删除")
	}
	if err := s.DiscardUnconfirmedDelivery(final); err != nil {
		t.Fatal(err)
	}
}

// TestDeliveryReservationPersists 验证未确认响应持续占有账单容量，直到终态落盘才允许其他请求使用。
// 使用有限容量临时队列；模拟执行器释放、并发准入与终态确认，测试后自动清理磁盘。
func TestDeliveryReservationPersists(t *testing.T) {
	s := openSpool(t, Config{MaxEntries: 4, MaxBytes: 8192, MaxRecordBytes: 4096})
	final := spoolRecord("delivery-capacity")
	final.Status, final.HTTPStatus, final.DeliveryPending, final.DeliverySpoolOnly = "success", 200, true, true
	if err := s.Reserve(final.RequestID); err != nil {
		t.Fatal(err)
	}
	marker, err := iam.UnconfirmedDelivery(final)
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	if err := s.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	s.Release(final.RequestID)
	status, err := s.Status()
	if err != nil || status.Reserved != 1 {
		t.Fatalf("未确认时提前释放容量: %+v %v", status, err)
	}
	if err := s.Reserve("other"); !errors.Is(err, ErrFull) {
		t.Fatalf("并发占用未受保护: %v", err)
	}
	if err := s.CompleteDelivery(final); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status()
	if err != nil || status.Reserved != 0 {
		t.Fatalf("终态后未释放容量: %+v %v", status, err)
	}
	if err := s.Reserve("other"); err != nil {
		t.Fatalf("释放后仍不能准入: %v", err)
	}
}

// TestSpoolConcurrentAdmission 验证在途请求按最大记录尺寸保守预留，多个并发准入不会超过容量。
// 前置仅容纳两个最大记录的临时队列，32个并发申请只允许两个；释放后可复用，无外部资源。
func TestSpoolConcurrentAdmission(t *testing.T) {
	s := openSpool(t, Config{MaxEntries: 2, MaxBytes: 8192, MaxRecordBytes: 4096})
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Reserve(string(rune('a'+i))) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("并发容量失守: %d", accepted.Load())
	}
	s.mu.Lock()
	for id := range s.reserved {
		delete(s.reserved, id)
	}
	s.mu.Unlock()
	if err := s.Reserve("next"); err != nil {
		t.Fatal("释放后不能复用", err)
	}
	if err := s.Reserve("next"); err != nil {
		t.Fatal("重复准入多占容量", err)
	}
	status, _ := s.Status()
	if status.Reserved != 1 || status.ReservedBytes != 4096 {
		t.Fatalf("重复预留: %+v", status)
	}
}

// TestSpoolSafeDirectoryAndLock 验证拒绝共享目录、最终目录符号链接和错误应用目录，不移动/更改用户文件。
// 前置临时文件系统，检查进程锁及数据完整性；全部句柄与路径由测试清理。
func TestSpoolSafeDirectoryAndLock(t *testing.T) {
	s := openSpool(t, Config{})
	if second, err := Open(s.config); err == nil {
		second.Close()
		t.Fatal("两个实例持有同一spool")
	}
	base := t.TempDir()
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if other, err := Open(Config{Dir: link}); err == nil {
		other.Close()
		t.Fatal("接受符号链接目录")
	}
	unrelated := filepath.Join(base, "unrelated")
	os.Mkdir(unrelated, 0755)
	os.WriteFile(filepath.Join(unrelated, "user.txt"), []byte("keep"), 0644)
	if other, err := Open(Config{Dir: unrelated}); err == nil {
		other.Close()
		t.Fatal("接受非专用目录")
	}
	content, err := os.ReadFile(filepath.Join(unrelated, "user.txt"))
	if err != nil || string(content) != "keep" {
		t.Fatal("修改其他目录用户文件")
	}
	info, _ := os.Stat(unrelated)
	if info.Mode().Perm() != 0755 {
		t.Fatal("改变其他目录权限")
	}
}

// TestSpoolRecoveryAndCorruption 验证完整临时记录恢复、坏校验/版本/身份/符号链接隔离，坏证据阻止新付费准入。
// 前置为模拟崩溃遗留文件，任何情况不读取外部链接目标；有效记录仍可恢复，临时目录自动清理。
func TestSpoolRecoveryAndCorruption(t *testing.T) {
	for _, mode := range []string{"temporary", "checksum", "version", "identity", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "spool")
			s := openSpool(t, Config{Dir: dir})
			record := spoolRecord("recover")
			raw, _ := encode(record)
			s.Close()
			name := basename(record.RequestID)
			switch mode {
			case "temporary":
				name = ".pending-crashed.tmp"
			case "checksum":
				raw = append(raw[:len(raw)-1], []byte(`,"checksum":"wrong"}`)...)
			case "version":
				var value map[string]any
				json.Unmarshal(raw, &value)
				value["version"] = 99
				raw, _ = json.Marshal(value)
			case "identity":
				name = basename("other")
			}
			if mode == "symlink" {
				outside := filepath.Join(t.TempDir(), "private")
				os.WriteFile(outside, []byte("do not read"), 0600)
				if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
			s = openSpool(t, Config{Dir: dir})
			status, _ := s.Status()
			if mode == "temporary" {
				if status.Pending != 1 || status.Quarantined != 0 {
					t.Fatalf("完整临时记录没有恢复: %+v", status)
				}
			} else {
				if status.Quarantined != 1 {
					t.Fatalf("坏记录未隔离: %+v", status)
				}
				if !errors.Is(s.Reserve("next"), ErrCorrupt) {
					t.Fatal("坏账单证据未阻止准入")
				}
			}
		})
	}
}

// TestSpoolReplayAfterConcurrentRemoval 在第一笔账单提交时处理同一批次的第二笔，稳定复现列目录后的竞争窗口。
// 两种已确认清理都只提交剩余账单，未经确认的文件消失必须报错；每个子用例独占临时目录并自动关闭、清理。
func TestSpoolReplayAfterConcurrentRemoval(t *testing.T) {
	for _, removal := range []string{"acknowledge", "discard-settled-delivery", "unexpected-loss"} {
		t.Run(removal, func(t *testing.T) {
			s := openSpool(t, Config{})
			records := []iam.UsageRecord{spoolRecord("first"), spoolRecord("second")}
			for _, record := range records {
				if err := s.Put(record); err != nil {
					t.Fatal(err)
				}
			}

			calls := 0
			count, err := s.Replay(t.Context(), 2, func(_ context.Context, current iam.UsageRecord) error {
				calls++
				other := records[0]
				if current.RequestID == other.RequestID {
					other = records[1]
				}
				// 不依赖文件系统的枚举顺序。两份文件均已列入本轮，而下一份尚未读取。
				switch removal {
				case "acknowledge":
					return s.Acknowledge(other)
				case "discard-settled-delivery":
					return s.DiscardUnconfirmedDelivery(other)
				default:
					return os.Remove(filepath.Join(s.config.Dir, basename(other.RequestID)))
				}
			})
			if count != 1 || calls != 1 {
				t.Fatalf("已处理的候选不能再次提交: count=%d callbacks=%d", count, calls)
			}
			if removal == "unexpected-loss" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("未经确认的文件消失必须返回读取错误: %v", err)
				}
			} else if err != nil {
				t.Fatalf("已确认入账的文件被误报为存储故障: %v", err)
			}
		})
	}
}

// TestSpoolFailureRetentionAndBackoff 验证数据库冲突保留原文且不阻塞其他有效记录，重复Flush不会忙重试同一失败。
// 前置两份快照，提交器模拟一份冲突一份成功；只确认成功记录，测试关闭并删除目录。
func TestSpoolFailureRetentionAndBackoff(t *testing.T) {
	s := openSpool(t, Config{})
	for _, id := range []string{"bad", "good"} {
		if err := s.Put(spoolRecord(id)); err != nil {
			t.Fatal(err)
		}
	}
	attempts := map[string]int{}
	commit := func(_ context.Context, r iam.UsageRecord) error {
		attempts[r.RequestID]++
		if r.RequestID == "bad" {
			return iam.ErrConflict
		}
		return nil
	}
	n, err := s.Replay(t.Context(), 10, commit)
	if n != 1 || err == nil {
		t.Fatalf("失败阻塞其他账单: %d %v", n, err)
	}
	n, err = s.Replay(t.Context(), 10, commit)
	if n != 0 || err != nil || attempts["bad"] != 1 {
		t.Fatalf("失败忙重试: %d %v %+v", n, err, attempts)
	}
	status, _ := s.Status()
	if status.Pending != 1 {
		t.Fatal("失败记录被删除")
	}
}

// TestSpoolLimitsPermissionsAndConflict 验证超大/非法记录拒绝、不安全目录权限拒绝、重复身份不同内容保留冲突证据。
// 前置临时磁盘和合成请求，不截断正文或覆盖先前快照；测试清理全部路径。
func TestSpoolLimitsPermissionsAndConflict(t *testing.T) {
	s := openSpool(t, Config{MaxBytes: 8192, MaxRecordBytes: 4096, MaxEntries: 4})
	record := spoolRecord("same")
	if err := s.Put(record); err != nil {
		t.Fatal(err)
	}
	changed := record
	changed.Cost = .99
	if !errors.Is(s.Put(changed), ErrConflict) {
		t.Fatal("相同身份不同费用被覆盖")
	}
	status, _ := s.Status()
	if status.Pending != 1 || status.Quarantined != 1 {
		t.Fatalf("冲突证据未保留: %+v", status)
	}
	if err := s.Acknowledge(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("错误快照确认删除原账单")
	}
	oversized := spoolRecord("oversized")
	oversized.RequestBody = strings.Repeat("x", 5000)
	if !errors.Is(s.Put(oversized), ErrFull) {
		t.Fatal("超大正文被截断/接受")
	}
	if err := s.Put(iam.UsageRecord{}); err == nil {
		t.Fatal("空记录被接受")
	}
	clean := openSpool(t, Config{})
	os.Chmod(clean.config.Dir, 0500)
	if err := clean.Reserve("denied"); err == nil {
		t.Fatal("只读目录准入未拒绝")
	}
	os.Chmod(clean.config.Dir, 0700)
}

// TestDeliveryMarkerRejectsInvalidAndCorruptTransitions 验证无效终态或损坏标记不会被静默覆盖、清除或重复结算。
// 使用独占临时磁盘队列，依次模拟非法状态、重复在途和文件损坏；修复后严格确认并清理，临时目录自动删除。
func TestDeliveryMarkerRejectsInvalidAndCorruptTransitions(t *testing.T) {
	s := openSpool(t, Config{})
	final := spoolRecord("transition-check")
	final.Status, final.HTTPStatus, final.DeliveryPending, final.DeliverySpoolOnly = "success", 200, true, true
	marker, err := iam.UnconfirmedDelivery(final)
	if err != nil {
		t.Fatal(err)
	}
	marker.DeliveryUnconfirmed = true
	if err := s.CompleteDelivery(final); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("缺少在途标记不能确认: %v", err)
	}
	invalid := marker
	invalid.RequestBody = "private"
	if err := s.BeginDelivery(invalid); err == nil {
		t.Fatal("未确认标记不能包含请求正文")
	}
	if err := s.BeginDelivery(marker); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginDelivery(marker); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复在途标记必须冲突: %v", err)
	}
	invalid = final
	invalid.DeliveryUnconfirmed = true
	if err := s.CompleteDelivery(invalid); err == nil {
		t.Fatal("未确认结果不能作为最终账单")
	}
	name := filepath.Join(s.config.Dir, basename(marker.RequestID))
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(final); err == nil {
		t.Fatal("损坏的标记不能被终态覆盖")
	}
	if err := s.DiscardUnconfirmedDelivery(final); err == nil {
		t.Fatal("损坏的标记不能被确认删除")
	}
	if err := os.WriteFile(name, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(final); err != nil {
		t.Fatalf("修复标记后应能确认: %v", err)
	}
	if err := s.DiscardUnconfirmedDelivery(final); err != nil {
		t.Fatalf("正确终态应能清理: %v", err)
	}
	if status, err := s.Status(); err != nil || status.Pending != 0 {
		t.Fatalf("确认后仍有残留: status=%+v err=%v", status, err)
	}
}

// replayWaitContext 让测试获知等待者已经进入 Replay 的可取消等待，无需用睡眠推断调度顺序。
type replayWaitContext struct {
	context.Context
	entered chan struct{}
	steps   chan struct{}
	once    sync.Once
}

// Done 返回原上下文的取消通知；首次监听时通知测试线程，渠道仅用于该测试实例。
func (c *replayWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	if c.steps != nil {
		c.steps <- struct{}{}
	}
	return c.Context.Done()
}

// TestReplaySharesOnlyCoveredWork 用阻塞提交器模拟前后台同轮恢复，验证每份只提交一次、
// 新发布的文件能被等待者观察，取消不影响持有者，失败原样返回；独占临时目录自动关闭清理。
func TestReplaySharesOnlyCoveredWork(t *testing.T) {
	for _, mode := range []string{"same", "new-record", "bounded-batch", "cancel", "failure", "failure-with-new-record", "panic"} {
		t.Run(mode, func(t *testing.T) {
			s := openSpool(t, Config{})
			if err := s.Put(spoolRecord("one")); err != nil {
				t.Fatal(err)
			}
			if mode == "bounded-batch" {
				if err := s.Put(spoolRecord("two")); err != nil {
					t.Fatal(err)
				}
			}
			started, resume := make(chan struct{}), make(chan struct{})
			persistErr := errors.New("fixture could not commit")
			first := make(chan error, 1)
			go func() {
				if mode == "panic" {
					defer func() {
						if recover() == persistErr {
							first <- persistErr
						}
					}()
				}
				limit := 8
				if mode == "bounded-batch" {
					limit = 1
				}
				_, err := s.Replay(t.Context(), limit, func(context.Context, iam.UsageRecord) error {
					close(started)
					<-resume
					if mode == "panic" {
						panic(persistErr)
					}
					if mode == "failure" || mode == "failure-with-new-record" {
						return persistErr
					}
					return nil
				})
				first <- err
			}()
			<-started
			if mode == "new-record" || mode == "failure-with-new-record" {
				if err := s.Put(spoolRecord("new")); err != nil {
					close(resume)
					t.Fatal(err)
				}
			}
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := &replayWaitContext{Context: base, entered: make(chan struct{})}
			type result struct {
				n   int
				err error
			}
			follower := make(chan result, 1)
			var commits atomic.Int32
			go func() {
				n, err := s.Replay(w, 8, func(context.Context, iam.UsageRecord) error { commits.Add(1); return nil })
				follower <- result{n, err}
			}()
			<-w.entered
			if mode == "cancel" {
				cancel()
				if got := <-follower; got.n != 0 || !errors.Is(got.err, context.Canceled) {
					t.Errorf("取消的等待者未退出: %+v", got)
				}
			}
			close(resume)
			err := <-first
			if mode == "panic" {
				got := <-follower
				if err != persistErr || !errors.Is(got.err, ErrUnavailable) {
					t.Fatalf("提交异常未唤醒等待者: %v %+v", err, got)
				}
				n, err := s.Replay(t.Context(), 1, func(context.Context, iam.UsageRecord) error { return nil })
				if err != nil || n != 1 {
					t.Fatalf("异常后的快照未可重放: %d %v", n, err)
				}
				return
			}
			if mode == "failure" || mode == "failure-with-new-record" {
				got := <-follower
				if !errors.Is(err, persistErr) || !errors.Is(got.err, persistErr) || commits.Load() != 0 {
					t.Fatalf("等待者未共享失败: %v %+v", err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				return
			}
			want := 0
			if mode == "new-record" || mode == "bounded-batch" {
				want = 1
			}
			if got := <-follower; got.err != nil || got.n != want || int(commits.Load()) != want {
				t.Fatalf("等待者重复提交或遗漏新工作: %+v 写入=%d 期望=%d", got, commits.Load(), want)
			}
		})
	}
}

// TestReplayWaitDoesNotChaseFutureWrites 固定请求到达时的快照水位，控制两个并行批次的完成顺序，
// 验证后来持续到达的快照不会迫使旧请求等第三个批次或继承其失败；所有文件由临时目录清理。
func TestReplayWaitDoesNotChaseFutureWrites(t *testing.T) {
	s := openSpool(t, Config{})
	if err := s.Put(spoolRecord("admitted")); err != nil {
		t.Fatal(err)
	}
	first := &replayBatch{generation: 0, done: make(chan struct{})}
	second := &replayBatch{generation: s.generation, done: make(chan struct{})}
	third := &replayBatch{done: make(chan struct{}), err: errors.New("later batch failed")}
	s.replaying = first
	ctx := &replayWaitContext{Context: t.Context(), entered: make(chan struct{}), steps: make(chan struct{}, 3)}
	done := make(chan error, 1)
	go func() {
		_, err := s.Replay(ctx, 10, func(context.Context, iam.UsageRecord) error {
			return errors.New("waiter unexpectedly started a new batch")
		})
		done <- err
	}()
	<-ctx.steps
	if err := s.Put(spoolRecord("newer")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	first.exhausted = true
	s.replaying = second
	close(first.done)
	s.mu.Unlock()
	<-ctx.steps
	s.mu.Lock()
	second.exhausted = true
	s.replaying = third
	close(third.done)
	close(second.done)
	s.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("当前请求误等待后来才发布的账单: %v", err)
	}
}

// awaitAdmissionTickets 等待指定数量的准入调用真实地进入临界区之前的取票阶段；
// 调用方暂持 mu 或阻塞探针。超时报告实际票数，不用固定睡眠猜测调度顺序。
func awaitAdmissionTickets(t *testing.T, s *Spool, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.admissionTicket.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("等待准入取票超时: 已进入 %d，期望 %d", s.admissionTicket.Load(), want)
		}
		runtime.Gosched()
	}
}

// TestReserveOnlySharesOverlappingProbes 用真实磁盘探针及已取票的并发请求检查复用次数。
// 每笔仍独立检查字节、条数、权限、隔离区；失败探针后必须重试。临时目录、权限和预留由夹具回收。
func TestReserveOnlySharesOverlappingProbes(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		config                  Config
		wantProbes, wantAllowed int
		wantErr                 error
	}{
		{"overlap", Config{}, 1, 4, nil},
		{"probe-failure", Config{}, 2, 3, ErrUnavailable},
		{"entry-cap", Config{MaxEntries: 2}, 1, 2, ErrFull},
		{"byte-cap", Config{MaxBytes: 8192, MaxRecordBytes: 4096}, 1, 2, ErrFull},
		{"directory-permissions", Config{}, 1, 1, ErrUnavailable},
		{"quarantine", Config{}, 1, 1, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openSpool(t, tc.config)
			defer os.Chmod(s.config.Dir, 0700)
			probes := 0
			probe := func() error {
				probes++ // 探针由同一把目录锁串行执行。
				if tc.name == "probe-failure" && probes == 1 {
					return ErrUnavailable
				}
				if err := s.probeWritableLocked(); err != nil {
					return err
				}
				if probes == 1 {
					switch tc.name {
					case "directory-permissions":
						return os.Chmod(s.config.Dir, 0500)
					case "quarantine":
						return os.WriteFile(filepath.Join(s.config.Dir, "quarantine", "corrupt"), []byte("fixture"), 0600)
					}
				}
				return nil
			}
			results := make(chan error, 4)
			func() {
				s.mu.Lock()
				defer s.mu.Unlock()
				for i := range 4 {
					go func() { results <- s.reserve(fmt.Sprintf("overlap-%d", i), probe) }()
				}
				awaitAdmissionTickets(t, s, 4)
			}()
			allowed := 0
			for range 4 {
				err := <-results
				if err == nil {
					allowed++
					continue
				}
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("准入拒绝类型错误: got=%v want=%v", err, tc.wantErr)
				}
			}
			if probes != tc.wantProbes || allowed != tc.wantAllowed || len(s.reserved) != allowed {
				t.Fatalf("探针或预留数量错误: probes=%d allowed=%d reserved=%d", probes, allowed, len(s.reserved))
			}
		})
	}
}

// TestReserveRequiresProbeStartedAfterArrival 让新请求在前一个探针已经开始、尚未完成时取票，
// 验证它要获得下一次真实探针；之后单独调用也要实测。独立队列自动关闭并清理。
func TestReserveRequiresProbeStartedAfterArrival(t *testing.T) {
	s := openSpool(t, Config{})
	started, allow := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	first := make(chan error, 1)
	go func() {
		first <- s.reserve("earlier", func() error {
			probes.Add(1)
			close(started)
			<-allow
			return s.probeWritableLocked()
		})
	}()
	<-started
	second := make(chan error, 1)
	probe := func() error { probes.Add(1); return s.probeWritableLocked() }
	go func() { second <- s.reserve("during-probe", probe) }()
	func() {
		defer close(allow)
		awaitAdmissionTickets(t, s, 2)
	}()
	if a, b := <-first, <-second; a != nil || b != nil || probes.Load() != 2 {
		t.Fatalf("探针开始后进入的请求复用了旧探针: first=%v second=%v probes=%d", a, b, probes.Load())
	}
	if err := s.reserve("later", probe); err != nil || probes.Load() != 3 {
		t.Fatalf("空闲后单次调用未重新探测: err=%v probes=%d", err, probes.Load())
	}
}

// TestSpoolErrorInvalidatesSharedProbe 在成功探针后制造一次真实确认冲突，再正常持久化一份新快照。
// 验证成功的其他写入无法清除故障后必须重新探针的要求；隔离文件及记录随临时目录回收。
func TestSpoolErrorInvalidatesSharedProbe(t *testing.T) {
	s := openSpool(t, Config{})
	original := spoolRecord("original")
	if err := s.Put(original); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve("earlier"); err != nil || s.probedThrough == 0 {
		t.Fatalf("缺少成功的实际探针: %v", err)
	}
	different := original
	different.Cost++
	if err := s.Acknowledge(different); !errors.Is(err, ErrConflict) {
		t.Fatalf("错误确认未被拒绝: %v", err)
	}
	if err := s.Put(spoolRecord("another")); err != nil {
		t.Fatal(err)
	}
	if s.probedThrough != 0 {
		t.Fatal("中途确认失败后仍可复用早先探针")
	}
	if err := s.Reserve("after-recovery"); err != nil || s.probedThrough == 0 {
		t.Fatalf("独立探针未能恢复: %v", err)
	}
}
