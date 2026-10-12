//go:build linux || darwin

package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func Test替换与撤销保留普通文件权限(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o755} {
		t.Run(fmt.Sprintf("%o", mode), func(t *testing.T) {
			_, w := openTestWorkspace(t)
			ctx := context.Background()
			target := filepath.Join(w.Root(), "plan.txt")
			writeExternal(t, target, "初始内容")
			if err := os.Chmod(target, mode); err != nil {
				t.Fatal(err)
			}
			before := snapshotForTest(t, w, "plan.txt")
			updated := "应用后的内容"
			operationID := model.NewID()
			r, err := w.Apply(ctx, operationID, "plan.txt", before, &updated)
			if err != nil || r.State != "applied" {
				t.Fatalf("应用应成功: %+v %v", r, err)
			}
			checkMode := func() {
				t.Helper()
				info, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != mode {
					t.Fatalf("应保留权限 %o，实际 %o", mode, info.Mode().Perm())
				}
			}
			checkMode()
			if err := w.Finalize(ctx, operationID, "plan.txt"); err != nil {
				t.Fatal(err)
			}
			undoID := model.NewID()
			r, err = w.Apply(ctx, undoID, "plan.txt", r.After, &before.Content)
			if err != nil || r.State != "applied" {
				t.Fatalf("撤销应成功: %+v %v", r, err)
			}
			checkMode()
			if bytesForTest(t, target) != before.Content {
				t.Fatal("撤销应恢复原正文")
			}
			if err := w.Finalize(ctx, undoID, "plan.txt"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func Test快照拒绝特殊权限位(t *testing.T) {
	for _, special := range []os.FileMode{os.ModeSetuid, os.ModeSetgid, os.ModeSticky} {
		t.Run(special.String(), func(t *testing.T) {
			_, w := openTestWorkspace(t)
			target := filepath.Join(w.Root(), "special.txt")
			writeExternal(t, target, "正文")
			if err := os.Chmod(target, 0o644|special); err != nil {
				t.Fatal(err)
			}
			_, err := w.Snapshot(context.Background(), "special.txt", ConditionalTextLimit)
			assertCode(t, err, model.CodeForbidden)
		})
	}
}

func Test冻结后权限变化拒绝替换并保留外部权限(t *testing.T) {
	_, w := openTestWorkspace(t)
	target := filepath.Join(w.Root(), "plan.txt")
	writeExternal(t, target, "原内容")
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotForTest(t, w, "plan.txt")
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	proposed := "候选内容"
	r, err := w.Apply(context.Background(), model.NewID(), "plan.txt", before, &proposed)
	assertCode(t, err, model.CodeConflict)
	if r.State != "conflict" || bytesForTest(t, target) != before.Content {
		t.Fatalf("外部权限变化后不得替换正文: %+v", r)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("必须保留外部新权限: %v", err)
	}
}

func snapshotForTest(t *testing.T, w *Workspace, rel string) Snapshot {
	t.Helper()
	s, err := w.Snapshot(context.Background(), rel, ConditionalTextLimit)
	if err != nil {
		t.Fatalf("读取条件快照应成功: %v", err)
	}
	return s
}
func bytesForTest(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
func writeExternal(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func Test条件创建替换保留原件与落账清理(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	before := snapshotForTest(t, w, "plan.md")
	if before.Exists {
		t.Fatal("新目标应不存在")
	}
	first := "第一版"
	id := model.NewID()
	r, err := w.Apply(ctx, id, "plan.md", before, &first)
	if err != nil || r.State != "applied" || !r.After.Exists {
		t.Fatalf("条件创建应成功: %+v %v", r, err)
	}
	if bytesForTest(t, filepath.Join(w.Root(), "plan.md")) != first {
		t.Fatal("创建正文不一致")
	}
	if info, err := os.Stat(filepath.Join(w.Root(), "plan.md")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限应为0600: %v", err)
	}
	if recovered, err := w.Recover(ctx, id, "plan.md", before, &first); err != nil || recovered.State != "applied" {
		t.Fatalf("恢复应识别本操作已安装的inode: %+v %v", recovered, err)
	}
	if err := w.Finalize(ctx, id, "plan.md"); err != nil {
		t.Fatal(err)
	}
	before = snapshotForTest(t, w, "plan.md")
	second := "第二版"
	id = model.NewID()
	r, err = w.Apply(ctx, id, "plan.md", before, &second)
	if err != nil || r.State != "applied" {
		t.Fatalf("替换应成功: %+v %v", r, err)
	}
	reserved := reservationName(id)
	original := filepath.Join(w.Root(), reserved, "original")
	if bytesForTest(t, original) != first {
		t.Fatal("落账前应保留原件")
	}
	_, err = w.ReadFile(ctx, reserved+"/original", 100)
	assertCode(t, err, model.CodeForbidden)
	entries, err := w.List(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name == reserved {
			t.Fatal("工具目录列表不应暴露保留区")
		}
	}
	if err := w.Finalize(ctx, id, "plan.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), reserved)); !os.IsNotExist(err) {
		t.Fatalf("落账后应清除保留目录: %v", err)
	}
}

func Test条件删除与撤销删除按缺失快照创建(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	if err := w.WriteFile(ctx, "created.txt", []byte("创建的内容")); err != nil {
		t.Fatal(err)
	}
	before := snapshotForTest(t, w, "created.txt")
	id := model.NewID()
	r, err := w.Apply(ctx, id, "created.txt", before, nil)
	if err != nil || r.State != "applied" || r.After.Exists {
		t.Fatalf("受控删除应保留原件并清空目标: %+v %v", r, err)
	}
	if bytesForTest(t, filepath.Join(w.Root(), reservationName(id), "original")) != before.Content {
		t.Fatal("删除也必须保留原件")
	}
	if recovered, err := w.Recover(ctx, id, "created.txt", before, nil); err != nil || recovered.State != "applied" {
		t.Fatalf("删除重启恢复应明确: %+v %v", recovered, err)
	}
	if err := w.Finalize(ctx, id, "created.txt"); err != nil {
		t.Fatal(err)
	}
	undoID := model.NewID()
	r, err = w.Apply(ctx, undoID, "created.txt", r.After, &before.Content)
	if err != nil || r.State != "applied" {
		t.Fatalf("恢复删除必须重新条件创建: %+v %v", r, err)
	}
	if err := w.Finalize(ctx, undoID, "created.txt"); err != nil {
		t.Fatal(err)
	}
}

func Test冻结后外部编辑在移动后被识别并恢复(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	target := filepath.Join(w.Root(), "a.txt")
	writeExternal(t, target, "预览内容")
	before := snapshotForTest(t, w, "a.txt")
	w.conditionalHooks = &conditionalHooks{beforeMove: func() { writeExternal(t, target, "外部最新编辑") }}
	next := "候选"
	id := model.NewID()
	r, err := w.Apply(ctx, id, "a.txt", before, &next)
	assertCode(t, err, model.CodeConflict)
	if r.State != "restored" || bytesForTest(t, target) != "外部最新编辑" {
		t.Fatalf("必须原样恢复移动时的真实条目: %+v", r)
	}
	if err := w.Finalize(ctx, id, "a.txt"); err != nil {
		t.Fatal(err)
	}
}

func Test移动原件后出现外部目标绝不覆盖(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	target := filepath.Join(w.Root(), "a.txt")
	writeExternal(t, target, "原件")
	before := snapshotForTest(t, w, "a.txt")
	w.conditionalHooks = &conditionalHooks{beforeInstall: func() { writeExternal(t, target, "外部新文件") }}
	next := "提议"
	id := model.NewID()
	r, err := w.Apply(ctx, id, "a.txt", before, &next)
	assertCode(t, err, model.CodeConflict)
	if r.State != "recovery_required" || bytesForTest(t, target) != "外部新文件" {
		t.Fatalf("不得覆盖外部新目标: %+v", r)
	}
	if bytesForTest(t, filepath.Join(w.Root(), reservationName(id), "original")) != "原件" {
		t.Fatal("原件必须保留")
	}
	assertCode(t, w.Finalize(ctx, id, "a.txt"), model.CodeConflict)
	if recovered, err := w.Recover(ctx, id, "a.txt", before, &next); err == nil || recovered.State != "recovery_required" {
		t.Fatalf("目标占用时不得猜测恢复成功: %+v %v", recovered, err)
	}
	if err := os.Rename(target, filepath.Join(w.Root(), "external-copy.txt")); err != nil {
		t.Fatal(err)
	}
	recovered, err := w.Recover(ctx, id, "a.txt", before, &next)
	if err != nil || recovered.State != "restored" || bytesForTest(t, target) != "原件" {
		t.Fatalf("释放目标后只能原样恢复原件: %+v %v", recovered, err)
	}
	if err := w.Finalize(ctx, id, "a.txt"); err != nil {
		t.Fatal(err)
	}
}

func Test条件创建遇到同内容外部文件也不猜测成功(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	target := filepath.Join(w.Root(), "new.txt")
	before := snapshotForTest(t, w, "new.txt")
	next := "同样内容"
	w.conditionalHooks = &conditionalHooks{beforeInstall: func() { writeExternal(t, target, next) }}
	id := model.NewID()
	r, err := w.Apply(ctx, id, "new.txt", before, &next)
	assertCode(t, err, model.CodeConflict)
	if r.State != "restored" || bytesForTest(t, target) != next {
		t.Fatalf("外部同内容文件不能当作本次安装: %+v", r)
	}
	if recovered, err := w.Recover(ctx, id, "new.txt", before, &next); err != nil || recovered.State == "applied" {
		t.Fatalf("恢复必须遵守真实操作记录: %+v %v", recovered, err)
	}
	if err := w.Finalize(ctx, id, "new.txt"); err != nil {
		t.Fatal(err)
	}
}

func Test安装后取消可通过候选inode恢复为已应用(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	writeExternal(t, filepath.Join(w.Root(), "a.txt"), "旧正文")
	before := snapshotForTest(t, w, "a.txt")
	next := "新正文"
	id := model.NewID()
	w.conditionalHooks = &conditionalHooks{afterInstall: cancel}
	r, err := w.Apply(ctx, id, "a.txt", before, &next)
	if err == nil || r.State != "recovery_required" {
		t.Fatalf("落账前取消应保留不确定状态: %+v %v", r, err)
	}
	recovered, err := w.Recover(context.Background(), id, "a.txt", before, &next)
	if err != nil || recovered.State != "applied" {
		t.Fatalf("重启检查应识别候选inode和hash: %+v %v", recovered, err)
	}
	if err := w.Finalize(context.Background(), id, "a.txt"); err != nil {
		t.Fatal(err)
	}
}

func Test快照拒绝不安全条目与大小编码(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	target := filepath.Join(w.Root(), "normal.txt")
	writeExternal(t, target, "内容")
	if err := os.Symlink("normal.txt", filepath.Join(w.Root(), "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(w.Root(), "hard.txt")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"link.txt", "normal.txt", "hard.txt", "inbound", "../outside", "/tmp/out", "inbound/../normal.txt", reservationPrefix + "x/original"} {
		_, err := w.Snapshot(ctx, rel, 100)
		if err == nil {
			t.Fatalf("应拒绝不安全快照路径 %s", rel)
		}
	}
	if err := os.Remove(filepath.Join(w.Root(), "hard.txt")); err != nil {
		t.Fatal(err)
	}
	_, err := w.Snapshot(ctx, "normal.txt", 1)
	assertCode(t, err, model.CodeInvalid)
	if err := os.WriteFile(target, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = w.Snapshot(ctx, "normal.txt", 100)
	assertCode(t, err, model.CodeInvalid)
	if err := os.Symlink("inbound", filepath.Join(w.Root(), "linked-dir")); err != nil {
		t.Fatal(err)
	}
	_, err = w.Snapshot(ctx, "linked-dir/missing.txt", 100)
	assertCode(t, err, model.CodeForbidden)
}

func Test保留区阻止普通写入删除及别名访问(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(w.Root(), "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeExternal(t, filepath.Join(w.Root(), "docs/a.txt"), "旧")
	before := snapshotForTest(t, w, "docs/a.txt")
	next := "新"
	id := model.NewID()
	if _, err := w.Apply(ctx, id, "docs/a.txt", before, &next); err != nil {
		t.Fatal(err)
	}
	reserved := "docs/" + reservationName(id)
	assertCode(t, w.WriteFile(ctx, reserved+"/original", []byte("不允许")), model.CodeForbidden)
	assertCode(t, w.Remove(ctx, "docs"), model.CodeConflict)
	if err := os.Symlink(filepath.Join(w.Root(), reserved), filepath.Join(w.Root(), "alias")); err != nil {
		t.Fatal(err)
	}
	_, err := w.ReadFile(ctx, "alias/original", 100)
	assertCode(t, err, model.CodeForbidden)
	_, err = w.SaveInbound(ctx, reservationPrefix+"bad", strings.NewReader("data"), 100)
	assertCode(t, err, model.CodeForbidden)
	if err := w.Finalize(ctx, id, "docs/a.txt"); err != nil {
		t.Fatal(err)
	}
}

func Test旧描述符继续写入必须保留原件供处理(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	target := filepath.Join(w.Root(), "a.txt")
	writeExternal(t, target, "原件")
	before := snapshotForTest(t, w, "a.txt")
	opened, err := os.OpenFile(target, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	w.conditionalHooks = &conditionalHooks{afterInstall: func() {
		if _, err := opened.WriteAt([]byte("外部"), 0); err != nil {
			t.Fatal(err)
		}
		if err := opened.Sync(); err != nil {
			t.Fatal(err)
		}
	}}
	next := "候选"
	id := model.NewID()
	r, err := w.Apply(ctx, id, "a.txt", before, &next)
	if err == nil || r.State != "recovery_required" {
		t.Fatalf("非协作旧描述符写入必须可见且不删原件: %+v %v", r, err)
	}
	assertCode(t, w.Finalize(ctx, id, "a.txt"), model.CodeConflict)
	if bytesForTest(t, filepath.Join(w.Root(), reservationName(id), "original")) == before.Content {
		t.Fatal("测试应确认外部写入仍被保留")
	}
}

func Test移动前取消不改变原文件(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	target := filepath.Join(w.Root(), "a.txt")
	writeExternal(t, target, "保留原稿")
	before := snapshotForTest(t, w, "a.txt")
	w.conditionalHooks = &conditionalHooks{beforeMove: cancel}
	next := "不应安装"
	id := model.NewID()
	r, err := w.Apply(ctx, id, "a.txt", before, &next)
	assertCode(t, err, model.CodeCancelled)
	if r.State != "unchanged" || bytesForTest(t, target) != before.Content {
		t.Fatalf("移动前取消不得改动原件: %+v", r)
	}
	if err := w.Finalize(context.Background(), id, "a.txt"); err != nil {
		t.Fatal(err)
	}
}

func Test更换父目录后旧快照不能应用(t *testing.T) {
	_, w := openTestWorkspace(t)
	dir := filepath.Join(w.Root(), "docs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExternal(t, filepath.Join(dir, "a.txt"), "原文")
	before := snapshotForTest(t, w, "docs/a.txt")
	if err := os.Rename(dir, filepath.Join(w.Root(), "old-docs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExternal(t, filepath.Join(dir, "a.txt"), "原文")
	next := "新文"
	r, err := w.Apply(context.Background(), model.NewID(), "docs/a.txt", before, &next)
	assertCode(t, err, model.CodeConflict)
	if r.State != "conflict" || bytesForTest(t, filepath.Join(dir, "a.txt")) != "原文" {
		t.Fatalf("不同父目录即使正文相同也必须拒绝: %+v", r)
	}
}

func Test第一份日志前中断可以确认未修改并清理(t *testing.T) {
	_, w := openTestWorkspace(t)
	ctx := context.Background()
	writeExternal(t, filepath.Join(w.Root(), "a.txt"), "原件")
	before := snapshotForTest(t, w, "a.txt")
	id := model.NewID()
	directory := filepath.Join(w.Root(), reservationName(id))
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeExternal(t, filepath.Join(directory, "journal-leftover.tmp"), "中断的日志")
	next := "提议"
	r, err := w.Recover(ctx, id, "a.txt", before, &next)
	if err != nil || r.State != "unchanged" {
		t.Fatalf("目标与原快照相同且未移动原件时应确认未修改: %+v %v", r, err)
	}
	if err := w.Finalize(ctx, id, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if bytesForTest(t, filepath.Join(w.Root(), "a.txt")) != before.Content {
		t.Fatal("恢复不应修改目标")
	}
}

func Test配置根的符号链接改指向后拒绝旧工作区对象(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, name := range []string{first, second} {
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
		writeExternal(t, filepath.Join(name, "a.txt"), "原文")
	}
	link := filepath.Join(dir, "selected")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	m := New(t.TempDir())
	w, err := m.Open(ctx, "agent", link)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotForTest(t, w, "a.txt")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	next := "不得写入"
	_, err = w.Apply(ctx, model.NewID(), "a.txt", before, &next)
	assertCode(t, err, model.CodeConflict)
	for _, name := range []string{first, second} {
		if bytesForTest(t, filepath.Join(name, "a.txt")) != "原文" {
			t.Fatal("根身份变化后不应写入任何目录")
		}
	}
}
