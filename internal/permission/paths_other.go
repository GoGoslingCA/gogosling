//go:build !windows

package permission

// canonicalPath 在非 Windows 平台上原样返回：没有 8.3 短名；大小写由 caseInsensitivePaths 处理，
// 符号链接由 resolveExisting 处理。
func canonicalPath(p string) string { return p }
