//go:build darwin

package workspace

import (
	"encoding/binary"
	"runtime"
	"strings"
	"unsafe"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"golang.org/x/sys/unix"
)

func renameExclusive(fromFD int, from string, toFD int, to string) error {
	return unix.RenameatxNp(fromFD, from, toFD, to, unix.RENAME_EXCL)
}

// darwinAttributeList 与系统 sys/attr.h 的 attrlist 固定布局一致。
type darwinAttributeList struct {
	BitmapCount, Reserved                 uint16
	Common, Volume, Directory, File, Fork uint32
}

// rejectUnsupportedMetadata 经已打开描述符读取 ACL，不用路径或外部命令重新定位文件。
// ATTR_CMN_EXTENDED_SECURITY 是 attrreference；长度为零才表示没有扩展安全结构。
// com.apple.provenance 由系统为新 inode 管理；其他扩展属性和 BSD flags 均不在本版保留范围内。
func rejectUnsupportedMetadata(fd int, st unix.Stat_t) error {
	if st.Flags != 0 {
		return conditionalError(model.CodeForbidden, "unsupported_metadata", nil)
	}
	attributes := darwinAttributeList{BitmapCount: 5, Common: unix.ATTR_CMN_EXTENDED_SECURITY}
	buffer := make([]byte, 8192)
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, uintptr(fd), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 0)
	runtime.KeepAlive(attributes)
	runtime.KeepAlive(buffer)
	if errno != 0 {
		return conditionalError(model.CodeUnavailable, "metadata_unavailable", errno)
	}
	length := binary.LittleEndian.Uint32(buffer[:4])
	if length < 12 || length > uint32(len(buffer)) {
		return conditionalError(model.CodeUnavailable, "metadata_unavailable", nil)
	}
	if binary.LittleEndian.Uint32(buffer[8:12]) != 0 {
		return conditionalError(model.CodeForbidden, "unsupported_metadata", nil)
	}
	n, err := unix.Flistxattr(fd, nil)
	if err != nil {
		return conditionalError(model.CodeUnavailable, "metadata_unavailable", err)
	}
	if n == 0 {
		return nil
	}
	if n > 64<<10 {
		return conditionalError(model.CodeForbidden, "unsupported_metadata", nil)
	}
	names := make([]byte, n)
	n, err = unix.Flistxattr(fd, names)
	if err != nil {
		return conditionalError(model.CodeUnavailable, "metadata_unavailable", err)
	}
	for _, name := range strings.Split(string(names[:n]), "\x00") {
		if name != "" && name != "com.apple.provenance" {
			return conditionalError(model.CodeForbidden, "unsupported_metadata", nil)
		}
	}
	return nil
}
