//go:build darwin

package workspace

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
	"golang.org/x/sys/unix"
)

func Test快照拒绝文件与父目录ACL(t *testing.T) {
	for _, parentACL := range []bool{false, true} {
		name := "文件"
		if parentACL {
			name = "父目录"
		}
		t.Run(name, func(t *testing.T) {
			_, w := openTestWorkspace(t)
			target := filepath.Join(w.Root(), "acl.txt")
			writeExternal(t, target, "保留正文")
			aclTarget := target
			if parentACL {
				aclTarget = w.Root()
			}
			// 系统工具仅为临时测试数据设置 ACL；生产检查只用已打开描述符。
			if out, err := exec.Command("/bin/chmod", "+a", "everyone allow read", aclTarget).CombinedOutput(); err != nil {
				t.Fatalf("设置测试 ACL 应成功: %v %s", err, out)
			}
			_, err := w.Snapshot(context.Background(), "acl.txt", ConditionalTextLimit)
			assertCode(t, err, model.CodeForbidden)
			if bytesForTest(t, target) != "保留正文" {
				t.Fatal("拒绝 ACL 文件不得改动内容")
			}
		})
	}
}

func Test快照拒绝不可保留扩展属性(t *testing.T) {
	_, w := openTestWorkspace(t)
	target := filepath.Join(w.Root(), "metadata.txt")
	writeExternal(t, target, "原正文")
	if err := unix.Setxattr(target, "com.gogosling.test", []byte("test"), 0); err != nil {
		t.Fatal(err)
	}
	_, err := w.Snapshot(context.Background(), "metadata.txt", ConditionalTextLimit)
	assertCode(t, err, model.CodeForbidden)
}
