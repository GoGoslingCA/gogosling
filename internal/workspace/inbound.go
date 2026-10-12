package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const (
	// DefaultAttachmentName 是文件名净化后为空时使用的名字。
	DefaultAttachmentName = "attachment"
	// maxFilenameBytes 限制落盘文件名的字节数，为重名序号留出余量（常见文件系统上限 255）。
	maxFilenameBytes = 180
	// maxNameAttempts 是重名时尝试加序号的上限。
	maxNameAttempts = 1000
	// sniffBytes 是用于 MIME 探测的头部字节数（与 net/http.DetectContentType 一致）。
	sniffBytes = 512
	// octetStream 是无法判断类型时的回退 MIME。
	octetStream = "application/octet-stream"
)

// SaveInbound 把 r 的内容保存到 inbound/ 目录，返回可写入消息的附件描述。
//
// 文件名会被净化（去掉目录部分、控制字符与平台保留字符），重名时追加 " (n)" 序号；
// 名字的占位用 O_EXCL 原子创建，内容先写临时文件再 rename 覆盖占位，因此其他读者不会看到半写的文件。
// maxBytes 大于 0 时，超过限制返回 CodeInvalid 并清理临时文件。
func (w *Workspace) SaveInbound(ctx context.Context, name string, r io.Reader, maxBytes int64) (model.Attachment, error) {
	if err := checkCtx(ctx); err != nil {
		return model.Attachment{}, err
	}
	safe := SanitizeFilename(name)
	if reservedPath(safe) {
		return model.Attachment{}, conditionalError(model.CodeForbidden, "protected_reservation", nil)
	}
	// inbound/ 必须仍在工作区内（防止它被替换成逃逸的符号链接）。
	dir, err := w.Resolve(InboundDir)
	if err != nil {
		return model.Attachment{}, err
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return model.Attachment{}, model.Wrap(model.CodeInternal, "创建附件目录失败", err)
	}

	tmp, err := os.CreateTemp(dir, ".upload-*.tmp")
	if err != nil {
		return model.Attachment{}, model.Wrap(model.CodeInternal, "创建临时文件失败", err)
	}
	tmpName := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	sniff := &sniffReader{r: r}
	var src io.Reader = sniff
	if maxBytes > 0 {
		src = io.LimitReader(sniff, maxBytes+1)
	}
	n, err := io.Copy(tmp, src)
	if err != nil {
		discard()
		return model.Attachment{}, model.Wrap(model.CodeInternal, "写入附件失败", err)
	}
	if maxBytes > 0 && n > maxBytes {
		discard()
		return model.Attachment{}, tooLarge(safe, n, maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return model.Attachment{}, model.Wrap(model.CodeInternal, "同步附件失败", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return model.Attachment{}, model.Wrap(model.CodeInternal, "关闭附件失败", err)
	}

	if err := w.lockWrite(ctx); err != nil {
		_ = os.Remove(tmpName)
		return model.Attachment{}, err
	}
	defer w.unlockWrite()
	final, finalName, err := reserveName(dir, safe)
	if err != nil {
		_ = os.Remove(tmpName)
		return model.Attachment{}, err
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		_ = os.Remove(final)
		return model.Attachment{}, model.Wrap(model.CodeInternal, "落盘附件失败", err)
	}
	return model.Attachment{
		Name: finalName,
		Path: InboundDir + "/" + finalName,
		MIME: detectMIME(finalName, sniff.head()),
		Size: n,
	}, nil
}

// reserveName 在 dir 下用 O_EXCL 原子地占住一个未被使用的文件名：
// 先试原名，再依次试 "stem (1).ext"、"stem (2).ext"……
func reserveName(dir, name string) (fullPath, finalName string, err error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		// 形如 ".env" 的名字：整个名字就是主干，序号追加在末尾。
		stem, ext = ext, ""
	}
	for i := 0; i < maxNameAttempts; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		full := filepath.Join(dir, candidate)
		f, err := os.OpenFile(full, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
		if err == nil {
			_ = f.Close()
			return full, candidate, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", "", model.Wrap(model.CodeInternal, "创建附件失败", err)
		}
	}
	return "", "", model.Errorf(model.CodeConflict, "附件 %q 的重名过多", name)
}

// SanitizeFilename 把客户端给出的文件名净化为可以安全落盘的单个文件名：
// 只保留最后一段路径；去掉控制字符与无效的 UTF-8 字节；把 < > : " | ? * 替换为下划线；
// 去掉首尾空白与末尾的点；避开 Windows 保留设备名；超长时在保留扩展名的前提下按字符截断。
// 净化后为空时返回 DefaultAttachmentName。
func SanitizeFilename(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			// 无效的 UTF-8 字节，丢弃。
			continue
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.TrimRight(out, ". ")
	if out == "" {
		return DefaultAttachmentName
	}
	if isReservedWindowsName(out) {
		out = "_" + out
	}
	return truncateName(out, maxFilenameBytes)
}

// isReservedWindowsName 判断主干（第一个点之前）是否是 Windows 保留设备名。
func isReservedWindowsName(name string) bool {
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// truncateName 把文件名限制在 maxBytes 字节内：优先保留扩展名，主干在字符边界上截断。
func truncateName(name string, maxBytes int) string {
	if len(name) <= maxBytes {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 16 || ext == name {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	keep := maxBytes - len(ext)
	if keep < 0 {
		keep = 0
	}
	if keep > len(stem) {
		keep = len(stem)
	}
	for keep > 0 && keep < len(stem) && !utf8.RuneStart(stem[keep]) {
		keep--
	}
	stem = stem[:keep]
	if stem == "" {
		stem = DefaultAttachmentName
	}
	return stem + ext
}

// detectMIME 先按扩展名判断，再用内容嗅探；两者都不知道时返回 application/octet-stream。
// 返回值不带参数（如 charset）。
func detectMIME(name string, head []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return stripMIMEParams(t)
	}
	if len(head) == 0 {
		return octetStream
	}
	return stripMIMEParams(http.DetectContentType(head))
}

func stripMIMEParams(t string) string {
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

// sniffReader 透传读取，同时记录最前面的 sniffBytes 字节用于类型探测。
type sniffReader struct {
	r   io.Reader
	buf []byte
}

func (s *sniffReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 && len(s.buf) < sniffBytes {
		take := sniffBytes - len(s.buf)
		if take > n {
			take = n
		}
		s.buf = append(s.buf, p[:take]...)
	}
	return n, err
}

func (s *sniffReader) head() []byte { return s.buf }
