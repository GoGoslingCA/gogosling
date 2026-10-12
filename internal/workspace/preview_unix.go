//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package workspace

import (
	"os"
	"syscall"
)

func openPreviewFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
