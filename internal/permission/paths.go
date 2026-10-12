package permission

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// 本文件收拢路径事实的平台差异：大小写、Windows 设备路径前缀与 8.3 短名、符号链接。
//
// 判定只读地探测文件系统（Lstat / EvalSymlinks / GetLongPathName），不读写文件内容。
// 原则是"规范化只能让防护更严"：拿不准的路径保持原样，按越界处理。

// caseInsensitivePaths 表示当前平台的默认文件系统不区分大小写（macOS 默认的 APFS / HFS+、Windows NTFS）。
// 在这些平台上，路径类规则与内核数据目录防护按不区分大小写比较；deny 规则在所有平台上都不区分大小写（fail-closed）。
var caseInsensitivePaths = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// windowsPaths 表示路径按 Windows 语义解释（盘符、UNC、设备路径前缀）。
var windowsPaths = runtime.GOOS == "windows"

// driveAbsRe 匹配以盘符开头的绝对路径（C:\x、C:/x 或 C:）。
var driveAbsRe = regexp.MustCompile(`^[A-Za-z]:([\\/]|$)`)

// stripVerbatimPrefix 去掉 Windows 的设备路径前缀：
//
//	\\?\C:\x、\\.\C:\x、\??\C:\x（以及 / 写法）→ C:\x
//	\\?\UNC\srv\share\x                      → \\srv\share\x
//
// 只有前缀之后是盘符绝对路径或 UNC 时才去掉；\\.\PhysicalDrive0、\\.\pipe\x 这类设备名保持原样
// （它们按绝对路径处理，因此视为越出工作区），避免去掉前缀后被当成工作区相对路径。
func stripVerbatimPrefix(p string) string {
	if len(p) < 4 {
		return p
	}
	switch strings.ReplaceAll(p[:4], "/", `\`) {
	case `\\?\`, `\\.\`, `\??\`:
	default:
		return p
	}
	rest := p[4:]
	if len(rest) > 4 && strings.EqualFold(strings.ReplaceAll(rest[:4], "/", `\`), `UNC\`) {
		return `\\` + rest[4:]
	}
	if driveAbsRe.MatchString(rest) {
		return rest
	}
	return p
}

// existingPrefix 把 p 拆成"路径上已存在的最深祖先"与其后尚不存在的片段（按从外到内的顺序）。
// 连根都不存在时 ok 为 false。
func existingPrefix(p string) (base string, rest []string, ok bool) {
	probe := p
	var tail []string
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return p, nil, false
		}
		tail = append(tail, filepath.Base(probe))
		probe = parent
	}
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	return probe, tail, true
}

// rejoin 把尚不存在的片段拼回规范化后的祖先。
func rejoin(base string, rest []string) string {
	return filepath.Join(append([]string{base}, rest...)...)
}

// resolveExisting 解析 p 上已存在部分的符号链接（EvalSymlinks），不存在的尾部原样拼回；
// 无法解析时返回 p。Windows 上 EvalSymlinks 同时把 8.3 短名展开为长名。
func resolveExisting(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return p
	}
	base, rest, ok := existingPrefix(p)
	if !ok {
		return p
	}
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		return p
	}
	return rejoin(real, rest)
}

// pathWithinFold 判断 p 是否在 root 之下（含 root 本身）；fold 为 true 时不区分大小写。
func pathWithinFold(root, p string, fold bool) bool {
	if fold {
		root, p = strings.ToLower(root), strings.ToLower(p)
	}
	return pathWithin(root, p)
}
