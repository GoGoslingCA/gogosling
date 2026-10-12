package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// LockFileName 是数据目录下单实例锁文件的名称。
const LockFileName = "daemon.lock"

// errLocked 表示锁被另一个存活的进程持有。
var errLocked = errors.New("app: 锁已被占用")

// instanceLock 是数据目录的单实例锁。
//
// 文件里写持有者的 pid，便于报错与排查；"持有者是否存活"由操作系统的文件锁回答
// （POSIX flock / Windows 独占共享模式），进程崩溃时锁随文件句柄自动释放。
// 这比单纯比较 pid 可靠：pid 会被复用，一个早已退出的守护进程留下的 pid 可能恰好属于
// 另一个无关进程，纯 pid 检查会因此拒绝启动；而文件锁只在真正的持有者存活时才冲突。
type instanceLock struct {
	path string
	file *os.File
}

// acquireLock 获取 <dataDir>/daemon.lock：被存活进程持有时返回 CodeConflict（Details 含 pid 与 data_dir）；
// 上一个持有者已退出（锁文件残留）时直接接管。
func acquireLock(dataDir string) (*instanceLock, error) {
	path := filepath.Join(dataDir, LockFileName)
	// 打开与加锁之间文件可能被上一任持有者删除（它退出时会删锁文件），那样我们锁住的是一个
	// 已脱离路径的 inode；校验路径仍指向同一文件，否则重试。
	for attempt := 0; attempt < 5; attempt++ {
		f, err := openLockFile(path)
		if err != nil {
			if errors.Is(err, errLocked) {
				return nil, lockConflict(dataDir, readLockPID(path))
			}
			return nil, model.Wrap(model.CodeUnavailable, "打开单实例锁文件失败", err)
		}
		if !stillLinked(f, path) {
			_ = f.Close()
			continue
		}
		if err := writePID(f); err != nil {
			_ = f.Close()
			return nil, model.Wrap(model.CodeUnavailable, "写入单实例锁文件失败", err)
		}
		return &instanceLock{path: path, file: f}, nil
	}
	return nil, model.NewError(model.CodeConflict, "单实例锁文件反复被替换，请稍后重试")
}

// Release 删除锁文件并释放锁；可重复调用。
func (l *instanceLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := releaseLockFile(l.file, l.path)
	l.file = nil
	return err
}

// lockConflict 构造"已有守护进程在运行"的错误。
func lockConflict(dataDir string, pid int) error {
	details := map[string]any{"data_dir": dataDir}
	if pid > 0 {
		details["pid"] = pid
	}
	return &model.Error{
		Code:    model.CodeConflict,
		Message: "另一个守护进程正在使用该数据目录",
		Details: details,
	}
}

// stillLinked 报告文件句柄是否仍对应 path 上的文件。
func stillLinked(f *os.File, path string) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	pi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(fi, pi)
}

// writePID 把当前进程号写入锁文件（覆盖旧内容）。
func writePID(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return err
	}
	return f.Sync()
}

// readLockPID 尽力读取锁文件里的 pid；读不到返回 0。
func readLockPID(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(raw)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
