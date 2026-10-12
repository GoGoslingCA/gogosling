package workspace

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// AgentsDirName 是数据根目录下存放智能体工作区的子目录名（见架构文档 §7）。
	AgentsDirName = "agents"
	// InboundDir 是工作区内存放入站附件的子目录名；它随工作区一起创建，且不允许被删除。
	InboundDir = "inbound"

	// dirPerm / filePerm 是数据目录内新建目录与文件的权限（AGENTS.md §3）。
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

// Manager 负责定位并创建智能体工作区；同一实例协调工作区写入，可以并发使用。
type Manager struct {
	dataDir   string
	writeGate chan struct{}
}

// New 创建工作区管理器；dataDir 是数据根目录（config.Config.DataDir）。
func New(dataDir string) *Manager {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &Manager{dataDir: filepath.Clean(dataDir), writeGate: gate}
}

// AgentsDir 返回存放所有相对路径工作区的父目录 <dataDir>/agents。
func (m *Manager) AgentsDir() string {
	return filepath.Join(m.dataDir, AgentsDirName)
}

// Open 打开（必要时创建）某个智能体的工作区。
//
// workspaceDir 的解释与 model.Agent.WorkspaceDir 一致：为空时使用 agentID；
// 相对路径相对于 <dataDir>/agents/ 且不得越出该目录；绝对路径原样使用，但不允许是文件系统根。
// 根目录与 inbound/ 子目录以 0700 权限创建。
func (m *Manager) Open(ctx context.Context, agentID, workspaceDir string) (*Workspace, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(agentID) == "" {
		return nil, model.NewError(model.CodeInvalid, "智能体标识不能为空")
	}
	root, err := m.resolveRoot(agentID, workspaceDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, model.Wrap(model.CodeInternal, "创建工作区目录失败", err)
	}
	if err := os.MkdirAll(filepath.Join(root, InboundDir), dirPerm); err != nil {
		return nil, model.Wrap(model.CodeInternal, "创建附件目录失败", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, model.Wrap(model.CodeInternal, "解析工作区目录失败", err)
	}
	return &Workspace{agentID: agentID, root: root, realRoot: realRoot, writeGate: m.writeGate}, nil
}

// resolveRoot 把 WorkspaceDir 翻译成绝对路径；规则见 Open 的注释。
func (m *Manager) resolveRoot(agentID, dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = agentID
	}
	if strings.ContainsRune(dir, 0) {
		return "", model.NewError(model.CodeInvalid, "工作区目录包含非法字符")
	}
	if filepath.IsAbs(dir) {
		clean := filepath.Clean(dir)
		if filepath.Dir(clean) == clean {
			return "", model.NewError(model.CodeInvalid, "工作区目录不能是文件系统根目录")
		}
		return clean, nil
	}
	norm := strings.ReplaceAll(dir, "\\", "/")
	if strings.HasPrefix(norm, "/") || filepath.VolumeName(dir) != "" || hasDotDot(norm) {
		return "", model.Errorf(model.CodeInvalid, "工作区目录 %q 不合法", dir)
	}
	clean := filepath.Clean(filepath.FromSlash(norm))
	if clean == "." {
		return "", model.Errorf(model.CodeInvalid, "工作区目录 %q 不合法", dir)
	}
	return filepath.Join(m.AgentsDir(), clean), nil
}

// Workspace 是一个已打开的工作区：所有操作都以工作区相对路径表达，并被约束在根目录之内。
//
// 同一 Manager 的写操作互斥；普通 WriteFile 仍是覆盖语义，需条件保护时使用 Apply。
type Workspace struct {
	agentID string
	// root 是词法上的绝对根路径；realRoot 是 EvalSymlinks 之后的真实路径，用于逃逸判断。
	root             string
	realRoot         string
	writeGate        chan struct{}
	conditionalHooks *conditionalHooks
}

// AgentID 返回工作区所属的智能体。
func (w *Workspace) AgentID() string { return w.agentID }

// Root 返回工作区根目录的绝对路径。
func (w *Workspace) Root() string { return w.root }

// Entry 描述工作区内的一个文件或目录。Path 是以 "/" 分隔的工作区相对路径。
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	ModTime time.Time `json:"mod_time"`
}

// Resolve 把工作区相对路径转换为绝对路径。
//
// 以下情况返回 CodeForbidden：绝对路径（含盘符与 UNC）、任何 ".." 片段、
// 以及路径上已存在的部分经符号链接解析后逃出工作区根目录。
// "" 与 "." 表示根目录本身；"/" 与 "\" 都视为分隔符，因此同一规则在三个平台上一致。
func (w *Workspace) Resolve(rel string) (string, error) {
	if reservedPath(rel) {
		return "", conditionalError(model.CodeForbidden, "protected_reservation", nil)
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return "", err
	}
	abs := w.root
	if clean != "." {
		abs = filepath.Join(w.root, clean)
	}
	if err := w.checkEscape(rel, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// checkEscape 找到 abs 上已经存在的最深祖先，解析符号链接后确认它仍在工作区内。
// 尚不存在的尾部片段不可能是符号链接，因此只需检查已存在的部分。
func (w *Workspace) checkEscape(rel, abs string) error {
	probe := abs
	for probe != w.root {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		probe = filepath.Dir(probe)
	}
	real, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return model.Wrap(model.CodeForbidden, "路径 "+rel+" 经过无法解析的符号链接", err)
	}
	if !isWithin(w.realRoot, real) {
		return model.Errorf(model.CodeForbidden, "路径 %q 经符号链接越出工作区", rel)
	}
	if relative, err := filepath.Rel(w.realRoot, real); err == nil && reservedPath(relative) {
		return conditionalError(model.CodeForbidden, "protected_reservation", nil)
	}
	return nil
}

// ReadFile 读取整个文件；maxBytes 大于 0 时，超过该大小返回 CodeInvalid 而不是截断。
func (w *Workspace) ReadFile(ctx context.Context, rel string, maxBytes int64) ([]byte, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fsError("读取", rel, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fsError("读取", rel, err)
	}
	if info.IsDir() {
		return nil, model.Errorf(model.CodeInvalid, "%s 是目录", rel)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, tooLarge(rel, info.Size(), maxBytes)
	}
	var r io.Reader = f
	if maxBytes > 0 {
		// 文件可能在 Stat 之后继续增长，读取时再限制一次。
		r = io.LimitReader(f, maxBytes+1)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fsError("读取", rel, err)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, tooLarge(rel, int64(len(data)), maxBytes)
	}
	return data, nil
}

// WriteFile 原子地写入文件：先写同目录下的临时文件，fsync 后 rename 到目标路径，
// 因此读者永远不会看到半写的内容。父目录不存在时自动创建；目标是目录时返回 CodeInvalid。
// 若目标本身是符号链接，rename 会用普通文件替换该链接，而不是写入链接指向的位置。
func (w *Workspace) WriteFile(ctx context.Context, rel string, data []byte) error {
	if err := w.lockWrite(ctx); err != nil {
		return err
	}
	defer w.unlockWrite()
	if err := checkCtx(ctx); err != nil {
		return err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return err
	}
	if clean == "." {
		return model.NewError(model.CodeInvalid, "不能把工作区根目录当作文件写入")
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(abs); err == nil && info.IsDir() {
		return model.Errorf(model.CodeInvalid, "%s 是目录", rel)
	}
	if err := os.MkdirAll(filepath.Dir(abs), dirPerm); err != nil {
		return model.Wrap(model.CodeInternal, "创建父目录失败", err)
	}
	return writeAtomic(abs, data)
}

// writeAtomic 用"临时文件 + rename"把 data 写到 abs。
func writeAtomic(abs string, data []byte) error {
	dir, base := filepath.Split(abs)
	tmp, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return model.Wrap(model.CodeInternal, "创建临时文件失败", err)
	}
	tmpName := tmp.Name()
	fail := func(msg string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return model.Wrap(model.CodeInternal, msg, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("写入临时文件失败", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("同步临时文件失败", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return model.Wrap(model.CodeInternal, "关闭临时文件失败", err)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		_ = os.Remove(tmpName)
		return model.Wrap(model.CodeInternal, "替换目标文件失败", err)
	}
	return nil
}

// List 返回目录下的直接子项：目录在前，再按名称排序。符号链接按链接本身报告，不跟随。
func (w *Workspace) List(ctx context.Context, rel string) ([]Entry, error) {
	if err := checkCtx(ctx); err != nil {
		return nil, err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fsError("列出", rel, err)
	}
	if !info.IsDir() {
		return nil, model.Errorf(model.CodeInvalid, "%s 不是目录", rel)
	}
	dirents, err := os.ReadDir(abs)
	if err != nil {
		return nil, fsError("列出", rel, err)
	}
	base := filepath.ToSlash(clean)
	if base == "." {
		base = ""
	}
	out := make([]Entry, 0, len(dirents))
	for _, d := range dirents {
		if reservedPath(d.Name()) {
			continue
		}
		fi, err := d.Info()
		if err != nil {
			// 条目在 ReadDir 与 Info 之间被删除，跳过即可。
			continue
		}
		out = append(out, newEntry(base, fi.Name(), fi))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Stat 返回单个路径的信息；根目录的 Path 为 "."。
func (w *Workspace) Stat(ctx context.Context, rel string) (Entry, error) {
	if err := checkCtx(ctx); err != nil {
		return Entry{}, err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return Entry{}, err
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return Entry{}, fsError("查看", rel, err)
	}
	e := newEntry("", fi.Name(), fi)
	e.Path = filepath.ToSlash(clean)
	return e, nil
}

// Exists 判断路径是否存在（不跟随最后一级符号链接）。
func (w *Workspace) Exists(ctx context.Context, rel string) (bool, error) {
	if err := checkCtx(ctx); err != nil {
		return false, err
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(abs); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fsError("查看", rel, err)
	}
	return true, nil
}

// Remove 删除文件或整个目录树。工作区根目录与 inbound/ 目录本身受保护（CodeForbidden）；
// 删除符号链接只删除链接本身。
//
// inbound/ 的保护不只看名字：darwin / windows 上名字不区分大小写（Windows 上还忽略末尾的点与空格），
// 并且在删除前用 os.SameFile 与真实的 inbound/ 比较，"Inbound"、"INBOUN~1" 这类指向同一目录的写法都会被拒绝。
func (w *Workspace) Remove(ctx context.Context, rel string) error {
	if err := w.lockWrite(ctx); err != nil {
		return err
	}
	defer w.unlockWrite()
	if err := checkCtx(ctx); err != nil {
		return err
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return err
	}
	if clean == "." {
		return model.NewError(model.CodeForbidden, "不能删除工作区根目录")
	}
	if isInboundName(clean, caseInsensitiveFS, runtime.GOOS == "windows") {
		return model.NewError(model.CodeForbidden, "不能删除附件目录 "+InboundDir)
	}
	abs, err := w.Resolve(rel)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return fsError("删除", rel, err)
	}
	if w.isProtectedDir(fi) {
		return model.NewError(model.CodeForbidden, "不能删除附件目录 "+InboundDir)
	}
	if fi.IsDir() {
		if err := rejectReservationTree(ctx, abs); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(abs); err != nil {
		return fsError("删除", rel, err)
	}
	return nil
}

// caseInsensitiveFS 表示当前平台的默认文件系统不区分大小写（macOS 默认的 APFS / HFS+、Windows NTFS）。
var caseInsensitiveFS = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// isInboundName 判断规范化后的相对路径是否就是 inbound/ 本身。
// fold 时不区分大小写；trimDots 时（Windows）忽略末尾的点与空格（"inbound." 与 "inbound " 都指向 inbound）。
func isInboundName(clean string, fold, trimDots bool) bool {
	name := filepath.ToSlash(clean)
	if trimDots {
		name = strings.TrimRight(name, ". ")
	}
	if fold {
		return strings.EqualFold(name, InboundDir)
	}
	return name == InboundDir
}

// isProtectedDir 判断 fi（Lstat 的结果）是否就是工作区根目录或 inbound/ 目录本身。
// 文件系统层面的比较覆盖名字比较漏掉的写法（大小写、8.3 短名等）；符号链接本身不会与目录相同。
func (w *Workspace) isProtectedDir(fi fs.FileInfo) bool {
	if !fi.IsDir() {
		return false
	}
	// 根目录本身可以是符号链接（例如 macOS 的 /var → /private/var），按目标比较；inbound/ 按自身比较。
	if ri, err := os.Stat(w.root); err == nil && os.SameFile(fi, ri) {
		return true
	}
	if ii, err := os.Lstat(filepath.Join(w.root, InboundDir)); err == nil && os.SameFile(fi, ii) {
		return true
	}
	return false
}

// newEntry 由 FileInfo 构造 Entry；base 为父目录的工作区相对路径（根为 ""）。
func newEntry(base, name string, fi fs.FileInfo) Entry {
	p := name
	if base != "" {
		p = path.Join(base, name)
	}
	e := Entry{Name: name, Path: p, IsDir: fi.IsDir(), ModTime: fi.ModTime().UTC()}
	if !fi.IsDir() {
		e.Size = fi.Size()
	}
	return e
}

// cleanRel 规范化调用方给出的相对路径：返回本地分隔符形式，根目录返回 "."。
// 绝对路径与任何 ".." 片段都被拒绝（CodeForbidden）；NUL 字节被拒绝（CodeInvalid）。
func cleanRel(rel string) (string, error) {
	if strings.ContainsRune(rel, 0) {
		return "", model.NewError(model.CodeInvalid, "路径包含非法字符")
	}
	norm := strings.ReplaceAll(rel, "\\", "/")
	if norm == "" || norm == "." {
		return ".", nil
	}
	if strings.HasPrefix(norm, "/") || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", model.Errorf(model.CodeForbidden, "不允许使用绝对路径 %q", rel)
	}
	if hasDotDot(norm) {
		return "", model.Errorf(model.CodeForbidden, "路径 %q 越出工作区", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(norm))
	if clean == "." {
		return ".", nil
	}
	return clean, nil
}

// hasDotDot 判断以 "/" 分隔的路径是否含有 ".." 片段。
func hasDotDot(slashPath string) bool {
	for _, seg := range strings.Split(slashPath, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// isWithin 判断 p 是否等于 root 或位于 root 之下；Windows 上不区分大小写。
func isWithin(root, p string) bool {
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		p = strings.ToLower(p)
	}
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// checkCtx 把已经结束的 ctx 转成带错误码的错误。
func checkCtx(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	code := model.CodeCancelled
	if errors.Is(err, context.DeadlineExceeded) {
		code = model.CodeTimeout
	}
	return model.Wrap(code, "操作未完成", err)
}

// fsError 把文件系统错误映射到错误码；路径只给出工作区相对形式，不泄露绝对路径。
// 路径中间某一级是文件（ENOTDIR）时，对调用方而言目标同样"不存在"。
func fsError(op, rel string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return model.NotFound("file", rel)
	case errors.Is(err, fs.ErrPermission):
		return model.Wrap(model.CodeForbidden, "没有权限"+op+" "+rel, err)
	}
	return model.Wrap(model.CodeInternal, op+"失败: "+rel, err)
}

// tooLarge 构造"超过大小限制"的错误，细节里带上实际大小与上限供界面展示。
func tooLarge(rel string, size, max int64) error {
	return &model.Error{
		Code:    model.CodeInvalid,
		Message: rel + " 超过大小限制",
		Details: map[string]any{"path": rel, "size": size, "max_bytes": max},
	}
}
