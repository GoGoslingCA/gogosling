//go:build linux

package workspace

import (
	"errors"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"golang.org/x/sys/unix"
)

func renameExclusive(fromFD int, from string, toFD int, to string) error {
	return unix.Renameat2(fromFD, from, toFD, to, unix.RENAME_NOREPLACE)
}

// Linux 的 POSIX ACL 与文件 capabilities 都通过扩展属性保存；本版不复制任何此类元数据。
func rejectUnsupportedMetadata(fd int, _ unix.Stat_t) error {
	n, err := unix.Flistxattr(fd, nil)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		return nil
	}
	if err != nil {
		return conditionalError(model.CodeUnavailable, "metadata_unavailable", err)
	}
	if n != 0 {
		return conditionalError(model.CodeForbidden, "unsupported_metadata", nil)
	}
	return nil
}
