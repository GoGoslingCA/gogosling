//go:build windows

package permission

import (
	"path/filepath"
	"syscall"
)

// canonicalPath 把绝对路径上已存在部分的 8.3 短名（例如 SSH~1）展开为长名，不存在的尾部原样拼回；
// 失败时返回原路径。规则与内核数据目录防护都在长名上比较，短名因此绕不过 **/.ssh/** 之类的规则。
func canonicalPath(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return p
	}
	base, rest, ok := existingPrefix(p)
	if !ok {
		return p
	}
	long, err := longPathName(base)
	if err != nil {
		return p
	}
	return rejoin(long, rest)
}

// longPathName 调用 GetLongPathNameW。
func longPathName(p string) (string, error) {
	in, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, syscall.MAX_PATH)
	for {
		n, err := syscall.GetLongPathName(in, &buf[0], uint32(len(buf)))
		if err != nil {
			return "", err
		}
		if n < uint32(len(buf)) {
			return syscall.UTF16ToString(buf[:n]), nil
		}
		// 缓冲区不够时返回值是所需长度（含结尾 NUL）。
		buf = make([]uint16, n)
	}
}
