package extension

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestReadSkillResourceReturnsTextAndRejectsInvalidFiles(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	src := writeSkillDir(t, "reference-skill", "带引用的技能", "参阅 references/guide.md")
	if err := os.MkdirAll(filepath.Join(src, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"guide.md": "# 指南\n请核对来源。\n", "binary": "a\x00b", "invalid-utf8": "caf\xe9", "large": strings.Repeat("x", MaxSkillResourceBytes+1),
	} {
		if err := os.WriteFile(filepath.Join(src, "references", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ext, err := env.m.InstallSkill(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	content, err := env.m.ReadSkillResource(ctx, ext.ID, "references/guide.md")
	if err != nil || content != "# 指南\n请核对来源。\n" {
		t.Fatalf("应按技能根目录读取引用文本：%q / %v", content, err)
	}
	for _, path := range []string{"references/binary", "references/invalid-utf8", "references/large", "references"} {
		if _, err := env.m.ReadSkillResource(ctx, ext.ID, path); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("非普通文本资源或超限必须拒绝：%s / %v", path, err)
		}
	}
	for _, path := range []string{"../secrets.enc", "/etc/passwd", `C:\secret`, `\\host\secret`, ".", "references/../SKILL.md"} {
		if _, err := env.m.ReadSkillResource(ctx, ext.ID, path); !model.IsCode(err, model.CodeForbidden) {
			t.Errorf("绝对路径与父目录跳转必须拒绝：%s / %v", path, err)
		}
	}
	if _, err := env.m.ReadSkillResource(ctx, ext.ID, "missing.md"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("缺失资源应为 not_found：%v", err)
	}
	if _, err := env.m.Disable(ctx, ext.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.m.ReadSkillResource(ctx, ext.ID, "references/guide.md"); !model.IsCode(err, model.CodeDenied) {
		t.Fatalf("停用技能不得读取资源：%v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := env.m.ReadSkillResource(cancelled, ext.ID, "references/guide.md"); !model.IsCode(err, model.CodeCancelled) {
		t.Fatalf("取消后不得继续读取：%v", err)
	}
}

func TestReadSkillResourceRejectsSymlinksAndCorruptedInstallation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建符号链接需要额外权限")
	}
	ctx := context.Background()
	env := newTestEnv(t)
	ext, err := env.m.InstallSkill(ctx, writeSkillDir(t, "links", "链接测试", "正文"))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(env.dataDir, "secrets.enc")
	if err := os.WriteFile(outside, []byte("private-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ext.InstallDir, "linked-secret")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(SkillFile, filepath.Join(ext.InstallDir, "linked-local")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"linked-secret", "linked-local"} {
		if _, err := env.m.ReadSkillResource(ctx, ext.ID, path); !model.IsCode(err, model.CodeForbidden) {
			t.Fatalf("资源链接必须拒绝，不能读内核秘密或换用链接目标：%s / %v", path, err)
		}
	}
	ext.InstallDir = env.dataDir
	if err := env.repo.UpdateExtension(ctx, ext); err != nil {
		t.Fatal(err)
	}
	if _, err := env.m.ReadSkillResource(ctx, ext.ID, "secrets.enc"); !model.IsCode(err, model.CodeForbidden) {
		t.Fatalf("损坏安装目录记录不得暴露内核文件：%v", err)
	}
	ext.ID, ext.InstallDir = ".", env.m.extensionsRoot()
	env.repo.mu.Lock()
	env.repo.items[ext.ID] = cloneExt(ext)
	env.repo.mu.Unlock()
	if _, err := env.m.ReadSkillResource(ctx, ext.ID, "linked-secret"); !model.IsCode(err, model.CodeForbidden) {
		t.Fatalf("损坏的扩展 ID 不得把整个扩展根作为技能目录：%v", err)
	}
}

func TestReadSkillResourceDoesNotExecuteScripts(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	src := writeSkillDir(t, "script-text", "脚本文本", "读取 helper.py")
	script := "raise RuntimeError('must not execute')\n"
	if err := os.WriteFile(filepath.Join(src, "helper.py"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ext, err := env.m.InstallSkill(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.m.ReadSkillResource(ctx, ext.ID, "helper.py")
	if err != nil || got != script {
		t.Fatalf("脚本必须仅作为普通文本返回：%q / %v", got, err)
	}
}
