//go:build !windows

package extension

import (
	"os"
	"syscall"
)

// 非阻塞打开可避免被并发换成 FIFO 的资源让读取永久挂起；普通文件忽略该标志。
func openSkillResource(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
