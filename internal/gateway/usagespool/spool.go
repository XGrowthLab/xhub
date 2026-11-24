// Package usagespool 为最终用量提供独立于 PostgreSQL/Redis 的本机持久快照，不调用或重试上游。
package usagespool

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofrs/flock"
	"github.com/sunqirui1987/xhub/internal/iam"
)

const (
	// DefaultBytes 限制本机待结算文件与并发在途预留总量，默认一 GiB，不代表可跨磁盘故障恢复。
	DefaultBytes int64 = 1 << 30
	// DefaultEntries 限制待结算文件及在途请求数量，防止无界目录扫描。
	DefaultEntries = 8192
	// DefaultGroupSize 限制一批并行文件同步占用的文件描述符，不额外等待凑批。
	DefaultGroupSize = 16
	// MaxGroupSize 限制并行暂存文件占用的文件描述符，一批最多同时打开 64 份。
	MaxGroupSize = 64
	// DefaultRecordBytes 为每次准入保守预留的最大记录空间；完整正文不得静默截断以适应上限。
	DefaultRecordBytes int64 = 16 << 20
)

var (
	ErrUnavailable = errors.New("usage spool unavailable")
	ErrFull        = errors.New("usage spool capacity exhausted")
	ErrCorrupt     = errors.New("usage spool has quarantined evidence")
	ErrConflict    = errors.New("usage spool request snapshot conflict")
	ErrPending     = errors.New("usage spool batch still has unsettled records")
)

// Config 为启动时固定的磁盘目录、容量及合并策略；目录必须专属于一个网关实例，多个实例应挂不同持久卷。
type Config struct {
	// GroupSize 为每批操作上限；0 使用默认值，1 逐笔执行，Open 拒绝其他超出 1–64 的值。
	GroupSize      int
	Dir            string
	MaxBytes       int64
	MaxEntries     int
	MaxRecordBytes int64
}

// Status 只包含数量和健康状态，不暴露请求正文、费用归属或磁盘文件内容。
type Status struct {
	Pending, Reserved, Quarantined int
	// Replayable 排除当前实例仍在写响应的文件；容量仍由 Pending/Reserved 完整核算。
	Replayable           int
	Bytes, ReservedBytes int64
	Healthy              bool
}

// envelope 以版本和内容摘要保护完整 UsageRecord；摘要检测损坏，不是来自不可信调用方的授权签名。
type envelope struct {
	Version  int             `json:"version"`
	Checksum string          `json:"checksum"`
	Payload  json.RawMessage `json:"payload"`
}

// replayBatch 记录一次目录枚举中仍待检查的文件名。只在 Spool.mu 下访问，不复制账单正文。
type replayBatch struct {
	pending        map[string]bool
	generation     uint64
	done           chan struct{}
	err            error
	exhausted      bool
	leaderCanceled bool
	unsettled      error
}

// replayFailure 为尚未确认的文件保留上次失败及可重试时刻；前台能识别待结错误而不提前重试数据库。
type replayFailure struct {
	after time.Time
	cause error
}

// Spool 独占本机目录；mu 保护文件更新、容量预留、重放批次及失败重试时间。
type Spool struct {
	mu     sync.Mutex
	writes mutationQueue
	// admissionTicket 在等待 mu 前分配。probedThrough 只覆盖开始实写探针前已经取票的调用。
	admissionTicket atomic.Uint64
	probedThrough   uint64
	root            *os.Root
	lock            *flock.Flock
	config          Config
	reserved        map[string]bool
	activeDelivery  map[string]bool
	replaying       *replayBatch
	generation      uint64
	retry           map[string]replayFailure
	lastErr         error
	closed          bool
}

// Open 创建或打开专用目录并取得非阻塞进程锁，返回可恢复队列或错误；网关启动调用。
// 参数容量零值采用默认值；禁止根目录、最终路径符号链接和宽松权限，目录0700/文件0600，失败关闭已打开资源。
func Open(config Config) (*Spool, error) {
	if config.GroupSize == 0 {
		config.GroupSize = DefaultGroupSize
	}
	if config.GroupSize < 1 || config.GroupSize > MaxGroupSize {
		return nil, fmt.Errorf("%w: invalid group size", ErrUnavailable)
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = DefaultBytes
	}
	if config.MaxEntries == 0 {
		config.MaxEntries = DefaultEntries
	}
	if config.MaxRecordBytes == 0 {
		config.MaxRecordBytes = DefaultRecordBytes
	}
	if config.Dir == "" || config.MaxBytes < 1 || config.MaxEntries < 1 || config.MaxRecordBytes < 1 || config.MaxRecordBytes > config.MaxBytes {
		return nil, fmt.Errorf("%w: invalid configuration", ErrUnavailable)
	}
	absolute, err := filepath.Abs(config.Dir)
	if err != nil {
		return nil, err
	}
	if absolute == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: dedicated directory required", ErrUnavailable)
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: unsafe directory", ErrUnavailable)
	}
	config.Dir = absolute
	// 已有非空目录必须有本队列格式标记，避免配置误指向其他应用目录后改变其中数据。
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		markerPath := filepath.Join(absolute, ".spool-format")
		markerInfo, infoErr := os.Lstat(markerPath)
		if infoErr != nil || !markerInfo.Mode().IsRegular() || markerInfo.Size() > 64 {
			return nil, fmt.Errorf("%w: invalid format marker", ErrUnavailable)
		}
		marker, err := os.ReadFile(markerPath)
		if err != nil || string(marker) != "xhub-usage-spool-v1\n" {
			return nil, fmt.Errorf("%w: directory is not an initialized usage spool", ErrUnavailable)
		}
	}
	if err = os.Chmod(absolute, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	if info, err = root.Lstat(".lock"); err == nil && !info.Mode().IsRegular() {
		root.Close()
		return nil, fmt.Errorf("%w: unsafe lock", ErrUnavailable)
	} else if err != nil && !os.IsNotExist(err) {
		root.Close()
		return nil, err
	}
	lock := flock.New(filepath.Join(absolute, ".lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		lock.Close()
		root.Close()
		return nil, fmt.Errorf("%w: directory already in use", ErrUnavailable)
	}
	s := &Spool{
		root: root, lock: lock, config: config,
		reserved: make(map[string]bool), activeDelivery: make(map[string]bool),
		retry: make(map[string]replayFailure),
	}
	if len(entries) == 0 {
		marker, createErr := root.OpenFile(".spool-format", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if createErr != nil {
			s.Close()
			return nil, createErr
		}
		_, writeErr := marker.Write([]byte("xhub-usage-spool-v1\n"))
		err = errors.Join(writeErr, marker.Sync(), marker.Close(), s.syncDirectoryLocked("."))
		if err != nil {
			s.Close()
			return nil, err
		}
	}
	if err = root.Mkdir("quarantine", 0700); err != nil && !os.IsExist(err) {
		s.Close()
		return nil, err
	}
	if info, err = root.Lstat("quarantine"); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		s.Close()
		return nil, fmt.Errorf("%w: unsafe quarantine", ErrUnavailable)
	}
	if err = root.Chmod("quarantine", 0700); err != nil {
		s.Close()
		return nil, err
	}
	// 在第一次准入前恢复临时文件/隔离损坏记录；坏证据保留且阻止新付费调用，其他有效记录仍允许后台恢复。
	s.mu.Lock()
	err = s.scanLocked(true)
	s.mu.Unlock()
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Close 释放目录与进程锁，供进程退出和隔离重启测试调用；重复调用安全，不删除任何快照。
func (s *Spool) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.root.Close(), s.lock.Close())
}

// basename 用 RequestID 的 SHA256 生成固定安全文件名；参数不会成为目录路径，返回64位十六进制名。
func basename(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".json"
}

// token 生成临时文件随机后缀；失败返回错误，不能复用固定名称覆盖未确认快照。
func token() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

// syncDirectoryLocked 同步目录项变更，参数为根目录内相对名；持mu调用，返回打开/fsync/关闭错误。
func (s *Spool) syncDirectoryLocked(name string) error {
	dir, err := s.root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// encode 验证并序列化完整快照，保留 reservation ID、原时间和冻结价格；返回带校验摘要的不可变记录。
func encode(record iam.UsageRecord) ([]byte, error) {
	if record.RequestID == "" || record.TS.IsZero() || record.Cost < 0 || math.IsNaN(record.Cost) || math.IsInf(record.Cost, 0) || record.PromptTokens < 0 || record.CompletionTokens < 0 || (record.CachedTokens != nil && *record.CachedTokens < 0) {
		return nil, fmt.Errorf("invalid usage snapshot")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	return json.Marshal(envelope{Version: 1, Checksum: hex.EncodeToString(sum[:]), Payload: payload})
}

// decode 校验版本、摘要、请求ID与文件名及数值；参数只来自私有目录，错误不删除证据。
func decode(raw []byte, name string) (iam.UsageRecord, error) {
	var record iam.UsageRecord
	var value envelope
	if json.Unmarshal(raw, &value) != nil || value.Version != 1 {
		return record, fmt.Errorf("unsupported or invalid usage envelope")
	}
	sum := sha256.Sum256(value.Payload)
	if hex.EncodeToString(sum[:]) != value.Checksum {
		return record, fmt.Errorf("usage checksum mismatch")
	}
	if err := json.Unmarshal(value.Payload, &record); err != nil {
		return record, err
	}
	if _, err := encode(record); err != nil {
		return record, err
	}
	if name != "" && basename(record.RequestID) != name {
		return record, fmt.Errorf("usage identity mismatch")
	}
	return record, nil
}

// readLocked 限长读取普通0600文件，返回完整字节；拒绝符号链接、特殊文件及超限载荷，不跟随用户路径。
func (s *Spool) readLocked(name string) ([]byte, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > s.config.MaxRecordBytes {
		return nil, fmt.Errorf("unsafe or oversized usage file")
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, s.config.MaxRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > s.config.MaxRecordBytes {
		return nil, ErrFull
	}
	return raw, nil
}

// quarantineLocked 原子移走损坏/冲突证据并同步两个目录，返回文件系统错误；不读取符号链接目标或覆盖旧证据。
func (s *Spool) quarantineLocked(name string) error {
	suffix, err := token()
	if err != nil {
		return err
	}
	if err = s.root.Rename(name, "quarantine/"+suffix+".evidence"); err != nil {
		return err
	}
	return errors.Join(s.syncDirectoryLocked("."), s.syncDirectoryLocked("quarantine"))
}

// scanLocked 检查目录中的最终/临时记录；recover 为真时将完整临时文件发布，其余损坏证据隔离。
// Open及状态检查调用，持mu执行；未知普通文件也隔离，防止忽略可能的账单，不清理证据。
func (s *Spool) scanLocked(recover bool) error {
	dir, err := s.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" || name == ".spool-format" || name == "quarantine" {
			continue
		}
		temporary := strings.HasPrefix(name, ".pending-") && strings.HasSuffix(name, ".tmp")
		if strings.HasPrefix(name, ".probe-") {
			if err = s.root.Remove(name); err != nil {
				return err
			}
			if err = s.syncDirectoryLocked("."); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") && !temporary {
			return fmt.Errorf("unexpected file in usage spool")
		}
		raw, readErr := s.readLocked(name)
		expected := name
		if temporary {
			expected = ""
		}
		record, decodeErr := decode(raw, expected)
		if readErr != nil || decodeErr != nil {
			if err = s.quarantineLocked(name); err != nil {
				return err
			}
			continue
		}
		if temporary && recover {
			target := basename(record.RequestID)
			if old, oldErr := s.readLocked(target); oldErr == nil {
				previous, previousErr := decode(old, target)
				if previousErr == nil && previous.DeliveryUnconfirmed && previous.DeliverySpoolOnly && record.DeliveryPending && record.DeliverySpoolOnly && !record.DeliveryUnconfirmed && (record.Cost == 0 || record.Status == "success" && record.HTTPStatus < 400) && sameDeliveryOwner(previous, record) {
					// 完整终态临时文件在客户端写出后 fsync；崩溃发生在 rename 前时可安全发布。
					if err = s.root.Rename(name, target); err != nil {
						return err
					}
				} else if string(old) != string(raw) {
					if err = s.quarantineLocked(name); err != nil {
						return err
					}
				} else {
					if err = s.root.Remove(name); err != nil {
						return err
					}
				}
			} else if os.IsNotExist(oldErr) {
				if err = s.root.Rename(name, target); err != nil {
					return err
				}
			} else {
				return oldErr
			}
			if err = s.syncDirectoryLocked("."); err != nil {
				return err
			}
		}
	}
	return nil
}

// sameDeliveryOwner 校验磁盘标记与终态属于同一请求和计费范围；不比较费用或用户正文。
// 参数 a/b 为校验过的快照，返回是否可原子替换；扫描恢复与正常终态写入共用。
func sameDeliveryOwner(a, b iam.UsageRecord) bool {
	return a.RequestID == b.RequestID && a.KeyID == b.KeyID && a.OwnerType == b.OwnerType && a.UserID == b.UserID && a.TeamID == b.TeamID && a.ProjectID == b.ProjectID && a.OrganizationID == b.OrganizationID && a.Model == b.Model && (a.CallType == "" || a.CallType == b.CallType) && a.BudgetReservationID == b.BudgetReservationID
}

// statusLocked 汇总容量、可重放数量和隔离状态；持 mu 调用，在途文件占容量但不计入 Replayable。
// 返回统计或读取错误，不读取正文或对外暴露身份；Status 和准入调用共用。
func (s *Spool) statusLocked() (Status, error) {
	result := Status{Reserved: len(s.reserved), ReservedBytes: int64(len(s.reserved)) * s.config.MaxRecordBytes}
	dir, err := s.root.Open(".")
	if err != nil {
		return result, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.Name() == ".lock" || entry.Name() == ".spool-format" || entry.Name() == "quarantine" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return result, err
		}
		result.Pending++
		if !s.activeDelivery[entry.Name()] {
			result.Replayable++
		}
		result.Bytes += info.Size()
	}
	dir, err = s.root.Open("quarantine")
	if err != nil {
		return result, err
	}
	quarantine, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return result, err
	}
	result.Quarantined = len(quarantine)
	for _, entry := range quarantine {
		info, err := entry.Info()
		if err != nil {
			return result, err
		}
		result.Bytes += info.Size()
	}
	result.Healthy = !s.closed && s.lastErr == nil && result.Quarantined == 0
	return result, nil
}

// finishLocked 在磁盘操作退出时持锁清除失效探针，再归还互斥锁；result 指向当前调用的返回错误。
// 已排队的准入不能沿用中途读写失败之前的探针，即使后一次写入恢复了队列状态。
func (s *Spool) finishLocked(result *error) {
	if *result != nil {
		s.probedThrough = 0
	}
	s.mu.Unlock()
}

// Status 返回本机队列健康/容量快照，供准入和诊断测试使用；无调用方请求正文，关闭或读取失败返回错误。
func (s *Spool) Status() (status Status, err error) {
	s.mu.Lock()
	defer s.finishLocked(&err)
	if s.closed {
		return Status{}, ErrUnavailable
	}
	return s.statusLocked()
}

// Reserve 在付费外发前保守预留一个最大记录槽并验证真实写入/fsync，重复同callID不多占槽。
// 并发在途与待结算共同受字节/条数限制；磁盘满、权限错误、隔离证据时拒绝，调用结束必须Release。
// 预留限制本队列并发容量，不承诺抵抗外部进程在准入后耗尽磁盘或硬件故障。
func (s *Spool) Reserve(callID string) error {
	return s.reserve(callID, false, s.probeWritableLocked)
}

// ReserveForAttempt 为网关的本次出站尝试预留账单空间。callID 贯穿重试和回退，返回与 Reserve 相同的准入错误。
// 新请求直接取票；已有普通预留在同一把锁内重新校验，仍在交付中的同步账单继续占用其终态空间。
func (s *Spool) ReserveForAttempt(callID string) error {
	return s.reserve(callID, true, s.probeWritableLocked)
}

// reserve 为 callID 申请空间；renew 为真时替换上次普通尝试的预留，probe 在锁内验证可持久写入。
// 每个请求先取票，随后逐个检查权限与容量；只有在其取票后才开始并已成功的探针可以共享。
func (s *Spool) reserve(callID string, renew bool, probe func() error) (err error) {
	if callID == "" {
		return ErrUnavailable
	}
	ticket := s.admissionTicket.Add(1)
	s.mu.Lock()
	defer s.finishLocked(&err)
	if s.closed {
		return ErrUnavailable
	}
	if s.reserved[callID] {
		if !renew || s.activeDelivery[basename(callID)] {
			return nil
		}
		// 重试必须先归还普通预留再按当前容量、权限及可写性准入；校验失败也不能留下旧槽。
		delete(s.reserved, callID)
	}
	status, err := s.statusLocked()
	if err != nil {
		return err
	}
	if status.Quarantined > 0 {
		return ErrCorrupt
	}
	if status.Pending+status.Reserved >= s.config.MaxEntries || status.Bytes+status.ReservedBytes+s.config.MaxRecordBytes > s.config.MaxBytes {
		return ErrFull
	}
	info, err := s.root.Stat(".")
	if err != nil || info.Mode().Perm() != 0700 {
		return ErrUnavailable
	}
	if ticket > s.probedThrough || s.lastErr != nil {
		// 取票截止点必须先于写入。即使另一个调用在本次 fsync 返回前到达，
		// 只要探针已经开始，它仍需下一个实际探针，不能复用先前或空闲期的结果。
		through := s.admissionTicket.Load()
		if err := probe(); err != nil {
			s.lastErr = err
			return err
		}
		s.lastErr = nil
		s.probedThrough = through
	}
	s.reserved[callID] = true
	return nil
}

// probeWritableLocked 建立临时探针，写入后同步其数据和根目录，再删除探针，以验证当前卷可写。
// 由 reserve 在互斥锁内调用；任一步骤失败都返回错误，不发布本次探针的取票截止点。
func (s *Spool) probeWritableLocked() error {
	suffix, err := token()
	if err != nil {
		return err
	}
	name := ".probe-" + suffix
	file, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write([]byte("usage-spool-probe\n"))
	// 探针不承载账单，文件和新建目录项可在同一时间同步；二者都结束后才卸下目录项。
	// 删除若因断电未持久化，现有启动扫描会清理 .probe-*。账单发布与确认仍按原有顺序刷盘。
	syncErr := syncProbe(file.Sync, func() error { return s.syncDirectoryLocked(".") })
	closeErr := file.Close()
	removeErr := s.root.Remove(name)
	return errors.Join(writeErr, syncErr, closeErr, removeErr)
}

// syncProbe 仅供准入探针并行同步仍有目录项的文件与其父目录。调用方持有队列锁并保持文件打开。
// 两个参数分别执行文件同步和目录同步；等待二者结束，返回合并的错误，任一失败都不能放行。
func syncProbe(syncFile, syncDir func() error) error {
	fileDone := make(chan error, 1)
	go func() { fileDone <- syncFile() }()
	dirErr := syncDir()
	return errors.Join(dirErr, <-fileDone)
}

// Release 归还尚未写入的在途容量；无返回值，重复或未知callID安全，不删除持久文件。
func (s *Spool) Release(callID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeDelivery[basename(callID)] {
		return
	}
	delete(s.reserved, callID)
}

// Put 先写完整临时记录并fsync，再原子改名及dirfsync；返回成功仅表示快照可由新进程恢复，不表示账单已入库。
// 相同RequestID/内容幂等；冲突保留两份证据并返回错误；失败不截断正文，准入后的磁盘故障交由调用方尝试其他持久介质。
func (s *Spool) Put(record iam.UsageRecord) (err error) {
	raw, err := encode(record)
	if err != nil {
		return err
	}
	return s.submitMutation(putSnapshot, record, raw)
}

// putLocked 写入已编码快照；调用方持有队列锁，返回完整持久化、容量或快照冲突错误。
// 正常账单和同步响应的未确认标记共用容量检查，不能覆盖不同内容。
func (s *Spool) putLocked(record iam.UsageRecord, raw []byte) error {
	var err error
	defer delete(s.reserved, record.RequestID)
	if s.closed {
		return ErrUnavailable
	}
	if int64(len(raw)) > s.config.MaxRecordBytes {
		s.lastErr = ErrFull
		return ErrFull
	}
	name := basename(record.RequestID)
	if old, readErr := s.readLocked(name); readErr == nil {
		if string(old) == string(raw) {
			return nil
		}
		status, statusErr := s.statusLocked()
		if statusErr != nil {
			return statusErr
		}
		if status.Bytes+int64(len(raw)) > s.config.MaxBytes {
			return ErrFull
		}
		sum := sha256.Sum256(raw)
		conflictName := "quarantine/conflict-" + hex.EncodeToString(sum[:]) + ".evidence"
		if _, existsErr := s.root.Lstat(conflictName); os.IsNotExist(existsErr) {
			if err = s.writeLocked(raw, conflictName); err != nil {
				return err
			}
		} else if existsErr != nil {
			return existsErr
		}
		s.lastErr = ErrConflict
		return ErrConflict
	} else if !os.IsNotExist(readErr) {
		s.lastErr = readErr
		return readErr
	}
	status, err := s.statusLocked()
	if err != nil {
		return err
	}
	reserved := status.ReservedBytes
	if s.reserved[record.RequestID] {
		reserved -= s.config.MaxRecordBytes
	}
	if status.Pending+status.Reserved-boolInt(s.reserved[record.RequestID]) >= s.config.MaxEntries || status.Bytes+reserved+int64(len(raw)) > s.config.MaxBytes {
		s.lastErr = ErrFull
		return ErrFull
	}
	err = s.writeLocked(raw, name)
	// 即使写入失败，临时文件也可能需要恢复；已开始的旧批次不能替新证据作确认。
	s.generation++
	return err
}

// BeginDelivery 持久化零费用未确认标记并将其标记为当前实例在途；参数只包含脱敏账单元数据。
// 返回错误时不允许把标记视为持久化；在途标记不由后台重放，进程退出后才允许作为未知结果恢复。
func (s *Spool) BeginDelivery(record iam.UsageRecord) (err error) {
	if !record.DeliveryPending || !record.DeliverySpoolOnly || !record.DeliveryUnconfirmed || record.Cost != 0 || record.Status != "error" || record.RequestBody != "" || record.ResponseBody != "" || record.ProxyRequest != "" {
		return fmt.Errorf("invalid delivery marker")
	}
	raw, err := encode(record)
	if err != nil {
		return err
	}
	return s.submitMutation(beginMarker, record, raw)
}

// beginLocked 持锁保存已校验输入的未确认标记。仅成功持久化后设置在途所有权，并继续持有原预留。
func (s *Spool) beginLocked(record iam.UsageRecord, raw []byte) error {
	name := basename(record.RequestID)
	if s.activeDelivery[name] {
		return ErrConflict
	}
	reserved := s.reserved[record.RequestID]
	if err := s.putLocked(record, raw); err != nil {
		return err
	}
	if reserved {
		s.reserved[record.RequestID] = true
	}
	s.activeDelivery[name] = true
	return nil
}

// CompleteDelivery 原子替换当前实例持有的未确认标记为写出终态；参数为同一次调用的完整最终快照。
// 写入或目录同步失败时保留未知结果证据并阻止本实例重放；成功后后台可恢复最终账单。
func (s *Spool) CompleteDelivery(record iam.UsageRecord) (err error) {
	if !record.DeliveryPending || !record.DeliverySpoolOnly || record.DeliveryUnconfirmed {
		return fmt.Errorf("invalid delivery final snapshot")
	}
	if record.Cost > 0 && (record.Status != "success" || record.HTTPStatus >= 400) {
		return fmt.Errorf("invalid failed delivery charge")
	}
	raw, err := encode(record)
	if err != nil {
		return err
	}
	return s.submitMutation(completeMarker, record, raw)
}

// markerSizeLocked 校验当前在途标记及账单归属，返回原始文件长度用于容量核算；单笔与合并提交共用。
func (s *Spool) markerSizeLocked(record iam.UsageRecord) (int64, error) {
	name := basename(record.RequestID)
	if s.closed || !s.activeDelivery[name] {
		return 0, ErrUnavailable
	}
	oldRaw, err := s.readLocked(name)
	if err != nil {
		return 0, err
	}
	old, err := decode(oldRaw, name)
	if err != nil {
		return 0, err
	}
	if !old.DeliveryUnconfirmed || !sameDeliveryOwner(old, record) {
		return 0, ErrConflict
	}
	return int64(len(oldRaw)), nil
}

// completeLocked 持锁逐笔发布已确认终态；文件或目录同步失败时保留在途归属及预留。
func (s *Spool) completeLocked(record iam.UsageRecord, raw []byte) error {
	name := basename(record.RequestID)
	oldSize, err := s.markerSizeLocked(record)
	if err != nil {
		return err
	}
	status, err := s.statusLocked()
	if err != nil {
		return err
	}
	reservedBytes := status.ReservedBytes
	if s.reserved[record.RequestID] {
		reservedBytes -= s.config.MaxRecordBytes
	}
	if int64(len(raw)) > s.config.MaxRecordBytes || status.Bytes-oldSize+int64(len(raw))+reservedBytes > s.config.MaxBytes {
		return ErrFull
	}
	s.generation++
	if err := s.writeLocked(raw, name); err != nil {
		return err
	}
	delete(s.activeDelivery, name)
	delete(s.reserved, record.RequestID)
	return nil
}

// DiscardUnconfirmedDelivery 清理已由数据库严格结算的磁盘标记或相同终态。
// record 为数据库已确认的完整终态；仅归属一致的未确认标记或逐字相同的终态可删除。
// 调用方必须先完成严格结算；删除或目录同步失败返回错误，保留后续恢复机会。
func (s *Spool) DiscardUnconfirmedDelivery(record iam.UsageRecord) (err error) {
	s.mu.Lock()
	defer s.finishLocked(&err)
	if s.closed {
		return ErrUnavailable
	}
	name := basename(record.RequestID)
	raw, err := s.readLocked(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	marker, err := decode(raw, name)
	if err != nil {
		return err
	}
	if !sameDeliveryOwner(marker, record) {
		return ErrConflict
	}
	if !marker.DeliveryUnconfirmed {
		expected, err := encode(record)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, expected) {
			return ErrConflict
		}
	}
	if err := s.root.Remove(name); err != nil {
		return err
	}
	delete(s.activeDelivery, name)
	delete(s.reserved, record.RequestID)
	return s.syncSettledRemovalLocked(name)
}

// stageFile 在专用目录创建独立 v1 临时文件，写入完整快照并同步内容，返回文件名及错误。
// 单笔和合并写入使用同样的恢复格式；失败留下的完整或部分文件由启动扫描恢复或隔离。
func (s *Spool) stageFile(raw []byte) (string, error) {
	suffix, err := token()
	if err != nil {
		return "", err
	}
	temporary := ".pending-" + suffix + ".tmp"
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return "", err
	}
	return temporary, nil
}

// writeLocked 在本笔临时文件同步成功后才改名，再同步所有变更目录；持锁返回持久化或文件系统错误。
func (s *Spool) writeLocked(raw []byte, name string) error {
	temporary, err := s.stageFile(raw)
	if err != nil {
		s.lastErr = err
		return err
	}
	if err = s.root.Rename(temporary, name); err != nil {
		s.lastErr = err
		return err
	}
	if err = s.syncDirectoryLocked("."); err != nil {
		s.lastErr = err
		return err
	}
	if filepath.Dir(name) != "." {
		if err = s.syncDirectoryLocked(filepath.Dir(name)); err != nil {
			s.lastErr = err
			return err
		}
	}
	s.lastErr = nil
	return nil
}

// boolInt 将预留存在性转换成容量计数；只用于本包数值计算，无副作用。
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// syncSettledRemovalLocked 在已核对账单且已删除 name 后持锁调用。目录同步成功才从正在重放的批次移除旧候选。
// 同步失败原样返回；文件意外丢失或被隔离均不走此路径，重放仍会报告读取错误。
func (s *Spool) syncSettledRemovalLocked(name string) error {
	if err := s.syncDirectoryLocked("."); err != nil {
		return err
	}
	s.noteSettledLocked(name)
	return nil
}

// noteSettledLocked 在账单精确匹配、磁盘删除及目录同步全部成功后更新内存恢复状态；调用方持有 mu。
func (s *Spool) noteSettledLocked(name string) {
	delete(s.retry, name)
	if s.replaying != nil {
		delete(s.replaying.pending, name)
	}
}

// acknowledgeLocked 由请求结算及队列重放持锁调用，仅在 name 的当前内容与已入账 raw 相同后删除。
// 返回内容冲突、读取或目录同步错误；成功时同时通知正在遍历此文件的重放批次。
func (s *Spool) acknowledgeLocked(name string, raw []byte) error {
	current, err := s.readLocked(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(current) != string(raw) {
		return ErrConflict
	}
	if err = s.root.Remove(name); err != nil {
		return err
	}
	return s.syncSettledRemovalLocked(name)
}

// Acknowledge 在调用方确认账单事务成功后删除同一快照；record必须是Put时完整值，不可仅凭RequestID删除新内容。
func (s *Spool) Acknowledge(record iam.UsageRecord) (err error) {
	raw, err := encode(record)
	if err != nil {
		return err
	}
	return s.submitMutation(acknowledgeSnapshot, record, raw)
}

// Replay 至多提交limit条快照；commit只负责本地数据库结算，成功才确认，失败保留并至少30秒后再试。
// 参数ctx取消即停止，返回本轮成功数和合并错误；单条冲突不阻塞其他记录，不包含正文的错误可供运维观察。
// 本轮候选与目录枚举一起在锁内登记；提交期间释放锁，其他请求确认入账后可撤下旧候选。
func (s *Spool) Replay(ctx context.Context, limit int, commit func(context.Context, iam.UsageRecord) error) (int, error) {
	return s.replay(ctx, limit, commit, false)
}

// ReplayForAdmission 在付费请求准入前调用，参数与 Replay 相同。返回实际结算数及本次水位仍有待结证据的错误；
// 即使本轮由于退避或条数上限没有再次尝试，也不能将尚未核销的费用当作已结清。
func (s *Spool) ReplayForAdmission(ctx context.Context, limit int, commit func(context.Context, iam.UsageRecord) error) (int, error) {
	return s.replay(ctx, limit, commit, true)
}

// replay 供后台和准入共享同一次物理回放。requireSettled 指定是否必须结清进入时的水位；
// 等待者的上下文独立，只有完全由领先调用取消引起的错误可以立即接手重试。
func (s *Spool) replay(ctx context.Context, limit int, commit func(context.Context, iam.UsageRecord) error, requireSettled bool) (int, error) {
	if limit < 1 || commit == nil {
		return 0, ErrUnavailable
	}
	s.mu.Lock()
	// 只需覆盖该请求进入恢复时已发布的证据。持续到达的新请求不应反复移动等待目标。
	observed := s.generation
	for {
		if s.closed {
			s.mu.Unlock()
			return 0, ErrUnavailable
		}
		active := s.replaying
		if active == nil {
			batch := &replayBatch{done: make(chan struct{}), generation: s.generation}
			s.replaying = batch
			n, err := s.runReplay(ctx, limit, commit, batch)
			if err == nil && requireSettled {
				err = batch.unsettled
			}
			return n, err
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-active.done:
		}
		// 本次开始时已有但上一轮没看到的快照，以及达到批量上限的剩余文件，要重新枚举。
		// 其他调用者已处理的文件不计为本次成功数；共享失败，不能让并发准入跳过存储错误。
		if active.err != nil && !active.leaderCanceled {
			return 0, active.err
		}
		if active.exhausted && active.generation >= observed {
			if requireSettled {
				return 0, active.unsettled
			}
			return 0, nil
		}
		s.mu.Lock()
	}
}

// onlyReplayCancellation 判断回调错误的所有叶子是否都来自本轮 ctx 的取消原因；
// commit、确认及多文件批处理都可能包装或合并错误。混入任何其他错误时必须保留该失败。
func onlyReplayCancellation(err, canceled error) bool {
	if canceled == nil || err == nil {
		return false
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		causes := wrapped.Unwrap()
		for _, cause := range causes {
			if !onlyReplayCancellation(cause, canceled) {
				return false
			}
		}
		return len(causes) > 0
	case interface{ Unwrap() error }:
		return onlyReplayCancellation(wrapped.Unwrap(), canceled)
	default:
		return errors.Is(err, canceled)
	}
}

// runReplay 接管 Replay 持有的目录锁并执行一次批处理，向等待者广播处理结果。
// 提交回调意外退出也会唤醒等待者并保留文件；成功数只计入真正提交它们的调用。
func (s *Spool) runReplay(ctx context.Context, limit int, commit func(context.Context, iam.UsageRecord) error, batch *replayBatch) (n int, err error) {
	err = ErrUnavailable
	defer func() {
		s.mu.Lock()
		batch.err = err
		batch.leaderCanceled = onlyReplayCancellation(err, ctx.Err())
		// pending 仅包含本轮水位内未获耐久确认的终态。后台可跳过退避文件，
		// 但同批的准入调用必须取得其失败原因；条数上限也不能让剩余证据被当作已处理。
		for name := range batch.pending {
			if batch.unsettled != nil {
				break
			}
			if failure, ok := s.retry[name]; ok {
				batch.unsettled = failure.cause
			}
		}
		if batch.unsettled == nil && len(batch.pending) > 0 {
			batch.unsettled = ErrPending
		}
		batch.exhausted = n < limit && err == nil
		s.replaying = nil
		close(batch.done)
		s.finishLocked(&err)
	}()
	return s.replayLocked(ctx, limit, commit, batch)
}

// replayLocked 从持有 mu 的目录快照恢复至多 limit 项；扫描结束即放锁，提交函数在锁外执行。
// 它在所有返回路径都已释放 mu。Replay 统一向同批等待者广播结果，已确认删除可在提交过程中撤下候选。
func (s *Spool) replayLocked(ctx context.Context, limit int, commit func(context.Context, iam.UsageRecord) error, batch *replayBatch) (int, error) {
	if err := s.scanLocked(true); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	quarantine, err := s.root.Open("quarantine")
	if err == nil {
		var entries []os.DirEntry
		entries, err = quarantine.ReadDir(1)
		quarantine.Close()
		if errors.Is(err, io.EOF) {
			err = nil
		}
		if len(entries) != 0 {
			batch.unsettled = ErrCorrupt
		}
	}
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	dir, err := s.root.Open(".")
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	// scanLocked 已连同在途标记一起校验过完整目录。这里只冻结当前能重放的证据；
	// 在途标记以后转成终态时会推进 generation，由覆盖其发布时间的批次处理。
	ready := make([]string, 0, len(entries))
	batch.pending = make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".json") && !s.activeDelivery[name] {
			ready = append(ready, name)
			batch.pending[name] = true
		}
	}
	s.mu.Unlock()
	count, attempted := 0, 0
	var failures []error
	for _, name := range ready {
		if attempted >= limit {
			break
		}
		s.mu.Lock()
		if len(batch.pending) == 0 {
			// 只有实际入账且删除目录项已同步才会撤下候选，外部丢失文件不会走这里。
			s.mu.Unlock()
			break
		}
		if !batch.pending[name] {
			s.mu.Unlock()
			continue
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return count, errors.Join(append(failures, err)...)
		}
		if s.activeDelivery[name] {
			s.mu.Unlock()
			continue
		}
		if time.Now().Before(s.retry[name].after) {
			s.mu.Unlock()
			continue
		}
		raw, err := s.readLocked(name)
		s.mu.Unlock()
		if err != nil {
			failures = append(failures, err)
			continue
		}
		record, err := decode(raw, name)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		attempted++
		err = commit(ctx, record)
		s.mu.Lock()
		if err == nil {
			err = s.acknowledgeLocked(name, raw)
		}
		if err != nil {
			failure := fmt.Errorf("usage spool record %s: %w", name, err)
			// 领先请求自行取消时保留文件但不惩罚健康后继；数据库失败或混合冲突仍须退避。
			if !onlyReplayCancellation(err, ctx.Err()) && batch.pending[name] {
				s.retry[name] = replayFailure{after: time.Now().Add(30 * time.Second), cause: failure}
			}
			failures = append(failures, failure)
		} else {
			count++
		}
		s.mu.Unlock()
	}
	return count, errors.Join(failures...)
}
