package usagespool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAbandonedStreamProbeClosesCleanly 验证已通过护栏但路由随后失败的流式请求可以直接放弃探针。
// 在临时磁盘并发关闭实例并重新打开同一目录，检查探针退出、无预留或账单、可重新准入；目录由夹具回收。
func TestAbandonedStreamProbeClosesCleanly(t *testing.T) {
	s := openSpool(t, Config{})
	// 暂时占住目录锁，让关闭与后台探针都遇到真实的生命周期边界。
	s.mu.Lock()
	probe := s.StartAttemptProbe("abandoned")
	if probe == nil {
		s.mu.Unlock()
		t.Fatal("空闲专属探针未启动")
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	s.mu.Unlock()
	if err := <-closed; err != nil {
		t.Fatalf("关闭探针所属队列失败：%v", err)
	}
	<-probe.done
	if probe.err != nil {
		t.Fatalf("关闭中断了已开始的磁盘操作：%v", probe.err)
	}
	reopened := openSpool(t, s.config)
	state, err := reopened.Status()
	if err != nil || state.Pending != 0 || state.Reserved != 0 || state.Quarantined != 0 {
		t.Fatalf("放弃探针遗留了账单、槽位或异常文件：%+v %v", state, err)
	}
	if _, err := reopened.ReserveAfterProbe("abandoned", probe); err != nil || reopened.admissionTicket.Load() != 1 {
		t.Fatalf("新实例没有独立执行真实准入：%v", err)
	}
}

// TestStreamAttemptProbeTicket 验证提前刷盘只覆盖本队列本请求的一次准入，并在实际分配时读取待结账。
// 每个子用例使用真实临时磁盘；普通调用、重试、滞留及跨队列的证明都须重新取票，结束时关闭并清理。
func TestStreamAttemptProbeTicket(t *testing.T) {
	for _, mode := range []string{"current", "retry", "stale", "wrong-id", "wrong-spool", "absent"} {
		t.Run(mode, func(t *testing.T) {
			s := openSpool(t, Config{})
			target := s
			p := s.StartAttemptProbe("request")
			if p == nil {
				t.Fatal("空闲队列无法为请求开始磁盘探针")
			}
			<-p.done
			if p.err != nil || len(s.reserved) != 0 {
				t.Fatalf("探针应只验证可写性：err=%v slots=%d", p.err, len(s.reserved))
			}
			if mode == "wrong-spool" {
				target = openSpool(t, Config{})
			}
			if err := target.Put(spoolRecord("after-probe")); err != nil {
				t.Fatal(err)
			}
			id := "request"
			p.finished = time.Now()
			switch mode {
			case "stale":
				p.finished = p.finished.Add(-2 * attemptProbeMaxAge)
			case "wrong-id":
				id = "other"
			case "absent":
				p = nil
			}
			before := target.admissionTicket.Load()
			pending, err := target.ReserveAfterProbe(id, p)
			if err != nil || pending != 1 || len(target.reserved) != 1 {
				t.Fatalf("实际准入必须看到刚刚发布的账单：pending=%d err=%v slots=%d", pending, err, len(target.reserved))
			}
			wanted := before + 1
			if mode == "current" || mode == "retry" {
				wanted = before
			}
			if mode == "retry" {
				if _, err := target.ReserveAfterProbe(id, p); err != nil {
					t.Fatal(err)
				}
				wanted++
			}
			if got := target.admissionTicket.Load(); got != wanted {
				t.Fatalf("刷盘证明的适用范围错误：票号 %d，期望 %d", got, wanted)
			}
		})
	}
}

// TestStreamAttemptProbeFaults 使用临时目录验证提前探针期间及之后的存储故障、隔离、满容量和关闭。
// 真实写盘冲突必须保留原文件，未通过的请求不能占新槽；空调用及忙队列直接退回普通路径，夹具回收文件。
func TestStreamAttemptProbeFaults(t *testing.T) {
	for _, mode := range []string{"probe-error", "invalidation", "quarantine", "full", "permissions", "closed", "busy"} {
		t.Run(mode, func(t *testing.T) {
			s := openSpool(t, Config{MaxEntries: 1})
			if s.StartAttemptProbe("") != nil {
				t.Fatal("空标识不应启动探针")
			}
			if mode == "busy" {
				s.probeMu.Lock()
				p := s.StartAttemptProbe("work")
				s.probeMu.Unlock()
				if p != nil {
					t.Fatal("同一时刻启动了第二个专属探针")
				}
				return
			}
			filename := filepath.Join(s.config.Dir, s.probePath)
			if mode == "probe-error" {
				if err := os.WriteFile(filename, []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			p := s.StartAttemptProbe("work")
			if p == nil {
				t.Fatal("缺少本次请求的探针")
			}
			<-p.done
			var want error
			switch mode {
			case "probe-error":
				want = os.ErrExist
			case "invalidation":
				s.mu.Lock()
				s.invalidateProbeLocked()
				s.mu.Unlock()
			case "quarantine":
				if err := os.WriteFile(filepath.Join(s.config.Dir, "quarantine", "evidence"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				want = ErrCorrupt
			case "full":
				if err := s.Put(spoolRecord("other")); err != nil {
					t.Fatal(err)
				}
				want = ErrFull
			case "permissions":
				if err := os.Chmod(s.config.Dir, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(s.config.Dir, 0700) })
				want = ErrUnavailable
			case "closed":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if s.StartAttemptProbe("late") != nil {
					t.Fatal("关闭的队列仍启动探针")
				}
				want = ErrUnavailable
			}
			p.finished = time.Now()
			_, err := s.ReserveAfterProbe("work", p)
			if !errors.Is(err, want) || s.reserved["work"] != (want == nil) {
				t.Fatalf("故障的准入或槽位不符：err=%v 期望 %v，reserved=%v", err, want, s.reserved["work"])
			}
			if mode == "invalidation" && s.probedThrough < p.ticket {
				t.Fatal("上次证明失效后未重新验证磁盘")
			}
			if mode == "probe-error" {
				if b, err := os.ReadFile(filename); err != nil || string(b) != "existing" {
					t.Fatalf("失败探针破坏已有文件：%q %v", b, err)
				}
			}
		})
	}
}
