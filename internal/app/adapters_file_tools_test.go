package app

import (
	"context"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/extension"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/tools"
	"github.com/GoGoslingCA/gogosling/internal/workspace"
)

func TestWorkspaceAdapterSupportsFileDeletionAndPreservesProtection(t *testing.T) {
	ctx := context.Background()
	ws, err := workspace.New(t.TempDir()).Open(ctx, model.NewID(), "")
	if err != nil {
		t.Fatal(err)
	}
	adapter := workspaceAdapter{ws: ws}
	if err := adapter.WriteFile(ctx, "notes/a.txt", []byte("original")); err != nil {
		t.Fatal(err)
	}
	res, err := tools.NewDeleteFile().Execute(ctx, tools.RunContext{Workspace: adapter}, []byte(`{"path":"notes/a.txt"}`))
	if err != nil || res.IsError {
		t.Fatalf("生产适配器应向工具暴露删除能力：%+v / %v", res, err)
	}
	if _, err := adapter.Stat(ctx, "notes/a.txt"); !model.IsCode(err, model.CodeNotFound) {
		t.Fatalf("批准删除后文件应不存在：%v", err)
	}
	if err := adapter.Remove(ctx, "inbound"); !model.IsCode(err, model.CodeForbidden) {
		t.Fatalf("适配器必须保留附件目录保护：%v", err)
	}
	if err := adapter.Remove(ctx, "../outside"); !model.IsCode(err, model.CodeForbidden) {
		t.Fatalf("适配器必须保留工作区路径约束：%v", err)
	}
}

type resourceSkillStore struct {
	fakeSkills
	loadedResources []string
}

func (s *resourceSkillStore) ReadSkillResource(_ context.Context, id, path string) (string, error) {
	s.loadedResources = append(s.loadedResources, id+"/"+path)
	return "resource text", nil
}

func TestSkillResourceAdapterScopesResourcesToSelectedSkills(t *testing.T) {
	ctx := context.Background()
	store := &resourceSkillStore{fakeSkills: fakeSkills{skills: []extension.Skill{{ID: "01SKILL", Name: "Notes"}}}}
	adapter := skillLoaderAdapter{s: store}
	agent := &model.Agent{ID: "agent", Extensions: []string{"01SKILL"}}
	if ok, err := adapter.HasSkillResources(ctx, agent); err != nil || !ok {
		t.Fatalf("资源能力与已启用技能存在时应提供工具：%v / %v", ok, err)
	}
	content, err := adapter.LoadSkillResource(ctx, agent, " notes ", "references/guide.md")
	if err != nil || content != "resource text" || len(store.loadedResources) != 1 || store.loadedResources[0] != "01SKILL/references/guide.md" {
		t.Fatalf("按选中技能解析名称后读取相对资源：%q / %v / %v", content, err, store.loadedResources)
	}
	if _, err := adapter.LoadSkillResource(ctx, agent, "another-skill", "references/guide.md"); !model.IsCode(err, model.CodeNotFound) || len(store.loadedResources) != 1 {
		t.Fatalf("不在智能体清单内的技能不得读资源：%v / %v", err, store.loadedResources)
	}
	store.skills = nil
	if ok, err := adapter.HasSkillResources(ctx, agent); err != nil || ok {
		t.Fatalf("技能停用或取消选中后应隐藏工具：%v / %v", ok, err)
	}
	legacy := skillLoaderAdapter{s: &fakeSkills{skills: []extension.Skill{{ID: "01SKILL"}}}}
	if ok, err := legacy.HasSkillResources(ctx, agent); err != nil || ok {
		t.Fatalf("旧加载器没有资源能力时应保持兼容并隐藏工具：%v / %v", ok, err)
	}
}
