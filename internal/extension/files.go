package extension

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// MaxExtensionFiles 是一个扩展目录允许的文件数上限。
	MaxExtensionFiles = 2000
	// MaxExtensionBytes 是一个扩展目录允许的总大小上限。
	MaxExtensionBytes = 64 << 20
	// extensionsDirName 是数据目录下存放扩展的子目录名（架构文档 §7）。
	extensionsDirName = "extensions"
	// digestPrefix 是目录摘要的算法前缀。
	digestPrefix = "sha256:"

	dirPerm      = 0o700
	filePerm     = 0o600
	execFilePerm = 0o700
)

// dirStats 是扫描目录得到的统计。
type dirStats struct {
	Files int
	Bytes int64
}

// digestFS 计算 fsys 下全部常规文件的内容摘要："sha256:<hex>"。
//
// 参与摘要的是相对路径（斜杠分隔、排序后）、大小与内容；拒绝符号链接与其他特殊文件；
// 超过文件数或总大小上限时返回 CodeInvalid。空目录也有确定的摘要。
func digestFS(fsys fs.FS) (string, dirStats, error) {
	var (
		paths []string
		stats dirStats
	)
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		mode := d.Type()
		switch {
		case mode&fs.ModeSymlink != 0:
			return model.Errorf(model.CodeInvalid, "扩展目录不允许包含符号链接: %s", p)
		case d.IsDir():
			return nil
		case !mode.IsRegular():
			return model.Errorf(model.CodeInvalid, "扩展目录包含不支持的文件类型: %s", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		stats.Files++
		stats.Bytes += info.Size()
		if stats.Files > MaxExtensionFiles {
			return model.Errorf(model.CodeInvalid, "扩展目录文件数超过上限 %d", MaxExtensionFiles)
		}
		if stats.Bytes > MaxExtensionBytes {
			return model.Errorf(model.CodeInvalid, "扩展目录总大小超过上限 %d 字节", MaxExtensionBytes)
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		if model.CodeOf(err) == model.CodeInvalid {
			return "", stats, err
		}
		return "", stats, model.Wrap(model.CodeInvalid, "读取扩展目录失败", err)
	}
	sort.Strings(paths)

	h := sha256.New()
	var sizeBuf [8]byte
	for _, p := range paths {
		f, err := fsys.Open(p)
		if err != nil {
			return "", stats, model.Wrap(model.CodeInvalid, "读取扩展文件失败", err)
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return "", stats, model.Wrap(model.CodeInvalid, "读取扩展文件失败", err)
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		binary.BigEndian.PutUint64(sizeBuf[:], uint64(info.Size()))
		h.Write(sizeBuf[:])
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", stats, model.Wrap(model.CodeInvalid, "读取扩展文件失败", err)
		}
		f.Close()
		h.Write([]byte{0})
	}
	return digestPrefix + hex.EncodeToString(h.Sum(nil)), stats, nil
}

// copyFS 把 fsys 的内容复制到 dst（dst 必须不存在）。目录 0700；文件 0600，源文件带可执行位时为 0700。
func copyFS(fsys fs.FS, dst string) error {
	if err := os.Mkdir(dst, dirPerm); err != nil {
		return err
	}
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return model.Errorf(model.CodeInvalid, "扩展目录不允许包含符号链接: %s", p)
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.Mkdir(target, dirPerm)
		}
		if !d.Type().IsRegular() {
			return model.Errorf(model.CodeInvalid, "扩展目录包含不支持的文件类型: %s", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		perm := os.FileMode(filePerm)
		if info.Mode()&0o111 != 0 {
			perm = execFilePerm
		}
		return copyFile(fsys, p, target, perm)
	})
}

func copyFile(fsys fs.FS, src, dst string, perm os.FileMode) error {
	in, err := fsys.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extensionsRoot 返回扩展根目录（<dataDir>/extensions）。
func (m *Manager) extensionsRoot() string {
	return filepath.Join(m.dataDir, extensionsDirName)
}

// installDir 返回某个扩展的安装目录。
func (m *Manager) installDir(id string) string {
	return filepath.Join(m.extensionsRoot(), id)
}

// ensureRoot 创建扩展根目录（0700）。
func (m *Manager) ensureRoot() error {
	if err := os.MkdirAll(m.extensionsRoot(), dirPerm); err != nil {
		return model.Wrap(model.CodeInternal, "创建扩展目录失败", err)
	}
	return nil
}

// stageFiles 把 fsys 复制到 <root>/.tmp-<id>，成功后原子地改名为 <root>/<id>。
// 目标已存在时（升级）先把旧目录移到 .old-<id>，改名成功后再删除旧目录；任一步失败都清理临时目录。
func (m *Manager) stageFiles(fsys fs.FS, id string) (string, error) {
	if err := m.ensureRoot(); err != nil {
		return "", err
	}
	final := m.installDir(id)
	tmp := filepath.Join(m.extensionsRoot(), ".tmp-"+id)
	old := filepath.Join(m.extensionsRoot(), ".old-"+id)
	_ = os.RemoveAll(tmp)
	_ = os.RemoveAll(old)

	if err := copyFS(fsys, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		if model.CodeOf(err) == model.CodeInvalid {
			return "", err
		}
		return "", model.Wrap(model.CodeInternal, "复制扩展文件失败", err)
	}
	replaced := false
	if _, err := os.Lstat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			_ = os.RemoveAll(tmp)
			return "", model.Wrap(model.CodeInternal, "移走旧版本失败", err)
		}
		replaced = true
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.RemoveAll(tmp)
		if replaced {
			_ = os.Rename(old, final)
		}
		return "", model.Wrap(model.CodeInternal, "安装扩展文件失败", err)
	}
	if replaced {
		if err := os.RemoveAll(old); err != nil {
			m.log.Warn("删除扩展旧版本目录失败", "extension_id", id, "dir", old, "error", err)
		}
	}
	return final, nil
}

// removeInstallDir 删除扩展目录。只接受位于扩展根目录之下、名字与扩展 ID 一致的目录，
// 防止记录被篡改后删除别的位置。目录不存在不算错误。
func (m *Manager) removeInstallDir(ext *model.Extension) error {
	if ext.InstallDir == "" {
		return nil
	}
	want := m.installDir(ext.ID)
	if filepath.Clean(ext.InstallDir) != want {
		return model.Errorf(model.CodeInternal, "扩展 %s 的安装目录 %q 不在扩展根目录内，拒绝删除", ext.ID, ext.InstallDir)
	}
	if err := os.RemoveAll(want); err != nil {
		return model.Wrap(model.CodeInternal, "删除扩展目录失败", err)
	}
	return nil
}

// openSourceDir 校验安装来源目录并返回它的绝对路径与文件系统视图。
// 拒绝：不存在、不是目录、位于扩展根目录之内（自己装自己）。
func (m *Manager) openSourceDir(srcDir string) (string, fs.FS, error) {
	if strings.TrimSpace(srcDir) == "" {
		return "", nil, model.NewError(model.CodeInvalid, "来源目录不能为空")
	}
	abs, err := filepath.Abs(srcDir)
	if err != nil {
		return "", nil, model.Wrap(model.CodeInvalid, "来源目录路径不合法", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, model.Errorf(model.CodeNotFound, "来源目录 %s 不存在", abs)
		}
		return "", nil, model.Wrap(model.CodeInvalid, "来源目录路径不合法", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, model.Wrap(model.CodeInvalid, "读取来源目录失败", err)
	}
	if !info.IsDir() {
		return "", nil, model.Errorf(model.CodeInvalid, "来源 %s 不是目录", abs)
	}
	if root, err := filepath.EvalSymlinks(m.extensionsRoot()); err == nil {
		if rel, err := filepath.Rel(root, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", nil, model.NewError(model.CodeInvalid, "不能从扩展根目录内安装")
		}
	}
	return abs, os.DirFS(resolved), nil
}

// readFileLimited 读取文件，超过 maxBytes 时返回 CodeInvalid。
func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, model.Errorf(model.CodeInvalid, "%s 超过 %d 字节", filepath.Base(path), maxBytes)
	}
	return data, nil
}

// readFSFile 从 fs.FS 读取文件，超过 maxBytes 时返回 CodeInvalid。
func readFSFile(fsys fs.FS, name string, maxBytes int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, model.Errorf(model.CodeInvalid, "%s 超过 %d 字节", name, maxBytes)
	}
	return data, nil
}
