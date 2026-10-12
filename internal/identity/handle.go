package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// HandleFileName 是运行时句柄文件名。
const HandleFileName = "daemon.json"

const handlePerm = 0o600

// Handle 是守护进程写给客户端的运行时句柄：到哪里连、用什么令牌、是谁在跑。
type Handle struct {
	PID      int    `json:"pid"`
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
	Version  string `json:"version"`
	// StartedAt 是守护进程启动时间（UTC）；客户端可据此判断句柄是否来自更早的实例。
	StartedAt time.Time `json:"started_at"`
}

// Validate 检查句柄是否完整。
func (h Handle) Validate() error {
	switch {
	case h.PID <= 0:
		return model.NewError(model.CodeInvalid, "句柄缺少有效的 pid")
	case h.Endpoint == "":
		return model.NewError(model.CodeInvalid, "句柄缺少 endpoint")
	case h.Token == "":
		return model.NewError(model.CodeInvalid, "句柄缺少 token")
	}
	return nil
}

// HandleFile 读写数据目录下的 daemon.json。零值不可用，请用 NewHandleFile 创建。
type HandleFile struct {
	path string
}

// NewHandleFile 返回指向 <dataDir>/daemon.json 的句柄文件。
func NewHandleFile(dataDir string) HandleFile {
	return HandleFile{path: filepath.Join(dataDir, HandleFileName)}
}

// Path 返回句柄文件的绝对路径。
func (f HandleFile) Path() string { return f.path }

// Write 原子写入句柄：先写同目录下的临时文件（0600）再重命名覆盖，读者永远看不到半截文件。
// StartedAt 为零值时填入当前 UTC 时间。
func (f HandleFile) Write(ctx context.Context, h Handle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.StartedAt.IsZero() {
		h.StartedAt = time.Now().UTC()
	} else {
		h.StartedAt = h.StartedAt.UTC()
	}
	if err := h.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return model.Wrap(model.CodeInternal, "序列化句柄失败", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return model.Wrap(model.CodeInternal, "创建数据目录失败", err)
	}

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return model.Wrap(model.CodeInternal, "系统随机源不可用", err)
	}
	tmp := f.path + ".tmp-" + hex.EncodeToString(suffix[:])
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, handlePerm)
	if err != nil {
		return model.Wrap(model.CodeInternal, "创建临时句柄文件失败", err)
	}
	fail := func(msg string, e error) error {
		_ = file.Close()
		_ = os.Remove(tmp)
		return model.Wrap(model.CodeInternal, msg, e)
	}
	if _, err := file.Write(data); err != nil {
		return fail("写入句柄文件失败", err)
	}
	if err := file.Sync(); err != nil {
		return fail("同步句柄文件失败", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return model.Wrap(model.CodeInternal, "关闭句柄文件失败", err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		_ = os.Remove(tmp)
		return model.Wrap(model.CodeInternal, "替换句柄文件失败", err)
	}
	return nil
}

// Read 读取并校验句柄：文件不存在返回 CodeNotFound（守护进程未运行），内容损坏返回 CodeInvalid。
func (f HandleFile) Read(ctx context.Context) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Handle{}, model.NotFound("daemon_handle", f.path)
	}
	if err != nil {
		return Handle{}, model.Wrap(model.CodeInternal, "读取句柄文件失败", err)
	}
	var h Handle
	if err := json.Unmarshal(data, &h); err != nil {
		return Handle{}, model.Wrap(model.CodeInvalid, "句柄文件不是合法 JSON", err)
	}
	if err := h.Validate(); err != nil {
		return Handle{}, err
	}
	h.StartedAt = h.StartedAt.UTC()
	return h, nil
}

// Remove 删除句柄文件；文件不存在不报错。守护进程退出时调用，令牌不留在磁盘上。
func (f HandleFile) Remove(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(f.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return model.Wrap(model.CodeInternal, "删除句柄文件失败", err)
	}
	return nil
}
