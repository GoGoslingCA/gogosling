//go:build windows

package app

import (
	"errors"
	"os"
	"syscall"
)

// errorSharingViolation 是 Windows 的 ERROR_SHARING_VIOLATION：文件已被别的句柄以不兼容的共享模式打开。
const errorSharingViolation syscall.Errno = 32

// openLockFile 以"只允许他人读取"的共享模式打开锁文件：持有期间其他进程无法以写方式打开它，
// 进程退出（包括崩溃）时句柄关闭，锁自动释放。被占用时返回 errLocked。
func openLockFile(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, errLocked
		}
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// releaseLockFile 先关闭句柄再删除：Windows 不允许删除仍被独占打开的文件。
// 若关闭后另一个守护进程抢先打开了它，删除会因共享冲突失败——那正是我们希望保留的锁。
func releaseLockFile(f *os.File, path string) error {
	closeErr := f.Close()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errorSharingViolation) {
		return err
	}
	return closeErr
}
