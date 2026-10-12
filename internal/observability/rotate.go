package observability

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	logFilePerm = 0o600
	logDirPerm  = 0o700
	// backupTimeLayout 不含冒号，保证 Windows 上也是合法文件名；按字典序即按时间序。
	backupTimeLayout = "2006-01-02T15-04-05.000"
)

// rotatingFile 是按大小轮转的追加写文件，实现 io.WriteCloser，可并发写入。
//
// 轮转策略：当前文件 <base>.log 超过 maxSize 时改名为 <base>-<UTC 时间戳>.log，
// 然后重新创建当前文件；可选地在后台把改名后的文件压缩为 .gz；
// 最后按时间戳只保留最新的 maxBackups 个备份。
type rotatingFile struct {
	path       string
	dir        string
	stem       string // 不含扩展名的文件名，例如 gogoslingd
	ext        string // 扩展名，例如 .log
	maxSize    int64
	maxBackups int
	compress   bool
	now        func() time.Time

	mu     sync.Mutex
	f      *os.File
	size   int64
	closed bool
	bg     sync.WaitGroup // 后台压缩任务
}

func openRotatingFile(path string, maxSize int64, maxBackups int, compress bool) (*rotatingFile, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, logDirPerm); err != nil {
		return nil, fmt.Errorf("observability: 创建日志目录失败: %w", err)
	}
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	w := &rotatingFile{
		path:       path,
		dir:        dir,
		stem:       strings.TrimSuffix(name, ext),
		ext:        ext,
		maxSize:    maxSize,
		maxBackups: maxBackups,
		compress:   compress,
		now:        time.Now,
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingFile) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logFilePerm)
	if err != nil {
		return fmt.Errorf("observability: 打开日志文件失败: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("observability: 读取日志文件状态失败: %w", err)
	}
	w.f = f
	w.size = info.Size()
	return nil
}

// Write 追加写入；若写入后会超过上限且当前文件非空，先轮转。单条记录超过上限时仍然写入，不丢弃。
func (w *rotatingFile) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotateLocked(); err != nil {
			// 轮转失败不应丢日志：继续写当前文件，错误交给调用方（slog 会忽略）。
			n, werr := w.f.Write(p)
			w.size += int64(n)
			return n, errors.Join(err, werr)
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// Close 关闭当前文件并等待后台压缩完成；可重复调用。
func (w *rotatingFile) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	err := w.f.Close()
	w.mu.Unlock()
	w.bg.Wait()
	return err
}

func (w *rotatingFile) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("observability: 关闭日志文件失败: %w", err)
	}
	backup := w.backupName()
	if err := os.Rename(w.path, backup); err != nil {
		// 改名失败时重新打开原文件继续写，避免后续写入全部失败。
		if reopenErr := w.open(); reopenErr != nil {
			return errors.Join(err, reopenErr)
		}
		return fmt.Errorf("observability: 轮转日志文件失败: %w", err)
	}
	if err := w.open(); err != nil {
		return err
	}
	if w.compress {
		w.bg.Add(1)
		go func() {
			defer w.bg.Done()
			gzipFile(backup)
			w.mu.Lock()
			w.pruneLocked()
			w.mu.Unlock()
		}()
		return nil
	}
	w.pruneLocked()
	return nil
}

// backupName 生成不与现有文件（含 .gz 变体）冲突的备份文件名。
func (w *rotatingFile) backupName() string {
	ts := w.now().UTC().Format(backupTimeLayout)
	for i := 0; ; i++ {
		name := w.stem + "-" + ts
		if i > 0 {
			name += fmt.Sprintf("-%d", i)
		}
		candidate := filepath.Join(w.dir, name+w.ext)
		if !exists(candidate) && !exists(candidate+".gz") {
			return candidate
		}
	}
}

// pruneLocked 删除最旧的备份，只保留 maxBackups 个；同一备份的 .log 与 .log.gz 视为一个。
func (w *rotatingFile) pruneLocked() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	prefix := w.stem + "-"
	byStem := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		stem := strings.TrimSuffix(name, ".gz")
		if !strings.HasSuffix(stem, w.ext) {
			continue
		}
		stem = strings.TrimSuffix(stem, w.ext)
		byStem[stem] = append(byStem[stem], name)
	}
	if len(byStem) <= w.maxBackups {
		return
	}
	stems := make([]string, 0, len(byStem))
	for s := range byStem {
		stems = append(stems, s)
	}
	sort.Strings(stems) // 时间戳命名，字典序即时间序，最旧的在前
	for _, s := range stems[:len(stems)-w.maxBackups] {
		for _, name := range byStem[s] {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
}

// gzipFile 把 path 压缩为 path.gz 并删除原文件；失败时保留原文件、清理半成品。
func gzipFile(path string) {
	src, err := os.Open(path)
	if err != nil {
		return
	}
	defer src.Close()
	gzPath := path + ".gz"
	dst, err := os.OpenFile(gzPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, logFilePerm)
	if err != nil {
		return
	}
	zw := gzip.NewWriter(dst)
	zw.Name = filepath.Base(path)
	_, copyErr := io.Copy(zw, src)
	closeErr := errors.Join(zw.Close(), dst.Close())
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(gzPath)
		return
	}
	_ = src.Close()
	_ = os.Remove(path)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
