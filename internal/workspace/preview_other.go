//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package workspace

import "os"

func openPreviewFile(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
