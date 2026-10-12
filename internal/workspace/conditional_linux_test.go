//go:build linux

package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"golang.org/x/sys/unix"
)

func Test快照拒绝不可保留扩展属性(t *testing.T) {
	_, w := openTestWorkspace(t)
	target := filepath.Join(w.Root(), "metadata.txt")
	writeExternal(t, target, "原正文")
	if err := unix.Setxattr(target, "user.gogosling_test", []byte("test"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			t.Skip("临时文件系统不支持扩展属性")
		}
		t.Fatal(err)
	}
	_, err := w.Snapshot(context.Background(), "metadata.txt", ConditionalTextLimit)
	assertCode(t, err, model.CodeForbidden)
}
