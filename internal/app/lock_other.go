//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package app

import (
	"errors"
	"os"
)

// openLockFile 是没有可用文件锁时的退化实现：用独占创建表达"持有"。
// 这些平台不是发行目标；残留的锁文件需要人工删除。
func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

func releaseLockFile(f *os.File, path string) error {
	closeErr := f.Close()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return closeErr
}
