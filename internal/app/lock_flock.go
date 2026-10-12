//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package app

import (
	"errors"
	"os"
	"syscall"
)

// openLockFile 打开（必要时创建）锁文件并以非阻塞方式加排他 flock；被占用时返回 errLocked。
func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

// releaseLockFile 先删除路径再关闭句柄：删除时仍持有锁，后来者只会创建并锁住一个新文件。
func releaseLockFile(f *os.File, path string) error {
	rmErr := os.Remove(path)
	closeErr := f.Close()
	if rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return rmErr
	}
	return closeErr
}
