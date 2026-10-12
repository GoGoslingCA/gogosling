package extension

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// MaxSkillResourceBytes 是技能资源文本读取的上限；超限拒绝，不静默截断源文件。
const MaxSkillResourceBytes = 1 << 20

// ReadSkillResource 只读已启用技能中的普通 UTF-8 文件，包括 references 与 scripts 的文本。
// 资源路径相对于安装目录，拒绝绝对路径、父目录跳转与所有符号链接；绝不执行资源。
// 智能体是否选用了该技能由调用方先检查。本方法并发安全。
func (m *Manager) ReadSkillResource(ctx context.Context, id, rel string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", model.Wrap(model.CodeCancelled, resourceText("resource_cancelled"), err)
	}
	rel = strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")
	if rel == "." || !fs.ValidPath(rel) || strings.ContainsAny(rel, ":\x00") {
		return "", model.NewError(model.CodeForbidden, resourceText("resource_path_invalid"))
	}
	ext, err := m.repo.GetExtension(ctx, id)
	if err != nil {
		return "", err
	}
	if ext.Kind != model.ExtensionSkill {
		return "", model.NewError(model.CodeInvalid, resourceText("resource_not_skill"))
	}
	if !ext.Enabled {
		return "", model.NewError(model.CodeDenied, resourceText("resource_disabled"))
	}
	if !model.IsID(ext.ID) || filepath.Clean(ext.InstallDir) != m.installDir(ext.ID) {
		return "", model.NewError(model.CodeForbidden, resourceText("resource_install_invalid"))
	}
	// 从数据根的句柄向内打开，目录被并发替换或资源链接到密钥文件时都不能逃逸。
	dataRoot, err := os.OpenRoot(m.dataDir)
	if err != nil {
		return "", model.Wrap(model.CodeUnavailable, resourceText("resource_read_failed"), err)
	}
	defer dataRoot.Close()
	installRel := extensionsDirName + "/" + ext.ID
	if err := rejectResourceSymlinks(dataRoot, installRel); err != nil {
		return "", err
	}
	installInfo, err := dataRoot.Lstat(installRel)
	if err != nil || !installInfo.IsDir() {
		return "", model.NewError(model.CodeForbidden, resourceText("resource_install_invalid"))
	}
	root, err := dataRoot.OpenRoot(installRel)
	if err != nil {
		return "", resourceReadError(err)
	}
	defer root.Close()
	openedInstallInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(installInfo, openedInstallInfo) {
		return "", model.NewError(model.CodeForbidden, resourceText("resource_install_invalid"))
	}
	if err := rejectResourceSymlinks(root, rel); err != nil {
		return "", err
	}
	before, err := root.Lstat(rel)
	if err != nil {
		return "", resourceReadError(err)
	}
	if !before.Mode().IsRegular() {
		return "", model.NewError(model.CodeInvalid, resourceText("resource_regular_required"))
	}
	f, err := openSkillResource(root, rel)
	if err != nil {
		return "", resourceReadError(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", resourceReadError(err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return "", model.NewError(model.CodeInvalid, resourceText("resource_regular_required"))
	}
	if info.Size() > MaxSkillResourceBytes {
		return "", model.NewError(model.CodeInvalid, resourceText("resource_too_large"))
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSkillResourceBytes+1))
	if err != nil {
		return "", resourceReadError(err)
	}
	if err := ctx.Err(); err != nil {
		return "", model.Wrap(model.CodeCancelled, resourceText("resource_cancelled"), err)
	}
	if len(data) > MaxSkillResourceBytes {
		return "", model.NewError(model.CodeInvalid, resourceText("resource_too_large"))
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "", model.NewError(model.CodeInvalid, resourceText("resource_text_required"))
	}
	return string(data), nil
}

func rejectResourceSymlinks(root *os.Root, rel string) error {
	var prefix string
	for _, part := range strings.Split(rel, "/") {
		if prefix != "" {
			prefix += "/"
		}
		prefix += part
		info, err := root.Lstat(prefix)
		if err != nil {
			return resourceReadError(err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return model.NewError(model.CodeForbidden, resourceText("resource_symlink_denied"))
		}
	}
	return nil
}

func resourceText(key string) string { return i18n.T(i18n.LocaleZH, "file_tools."+key, nil) }

func resourceReadError(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return model.NewError(model.CodeNotFound, resourceText("resource_not_found"))
	}
	return model.Wrap(model.CodeForbidden, resourceText("resource_read_failed"), err)
}
