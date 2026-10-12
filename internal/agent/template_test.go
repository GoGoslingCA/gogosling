package agent

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestBuiltinTemplates(t *testing.T) {
	lib, err := BuiltinTemplates()
	if err != nil {
		t.Fatalf("加载内置模板失败: %v", err)
	}
	if lib.Len() != 2 {
		t.Fatalf("内置模板数量 = %d，期望 2", lib.Len())
	}
	for _, id := range []string{"general-assistant", "ops-engineer"} {
		tpl, ok := lib.Get(id)
		if !ok {
			t.Errorf("缺少模板 %s", id)
			continue
		}
		if tpl.Name == "" || tpl.Description == "" || tpl.SystemPrompt == "" || len(tpl.Tools) == 0 {
			t.Errorf("模板 %s 字段不完整: %+v", id, tpl)
		}
		if strings.HasPrefix(tpl.SystemPrompt, bom) || strings.Contains(tpl.SystemPrompt, "\r") || strings.HasSuffix(tpl.SystemPrompt, "\n") {
			t.Errorf("模板 %s 的提示未规范化", id)
		}
		if err := validateAutonomy(tpl.Autonomy); err != nil {
			t.Errorf("模板 %s 自主等级不合法: %v", id, err)
		}
		for _, tool := range tpl.Tools {
			if !contains(DefaultTools(), tool) {
				t.Errorf("模板 %s 引用了未知工具 %s", id, tool)
			}
		}
	}
	list := lib.List()
	if list[0].ID != "general-assistant" || list[1].ID != "ops-engineer" {
		t.Errorf("List 应按标识排序: %s, %s", list[0].ID, list[1].ID)
	}
	// Get 返回副本。
	tpl, _ := lib.Get("ops-engineer")
	tpl.Tools[0] = "changed"
	again, _ := lib.Get("ops-engineer")
	if again.Tools[0] == "changed" {
		t.Error("Get 应返回副本")
	}
	if _, ok := lib.Get("missing"); ok {
		t.Error("不存在的模板应返回 false")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestLoadTemplatesFromFS(t *testing.T) {
	fsys := fstest.MapFS{
		"alpha/template.json": {Data: []byte(`{"id":"alpha","name":"甲","tools":["read_file"," read_file "],"autonomy":"ask_all"}`)},
		"alpha/SOUL.md":       {Data: []byte(bom + "# 甲\r\n\r\n正文\r\n")},
		"beta/template.json":  {Data: []byte(`{"name":"乙"}`)},
		"README.md":           {Data: []byte("忽略非目录条目")},
	}
	lib, err := LoadTemplates(fsys)
	if err != nil {
		t.Fatalf("LoadTemplates 失败: %v", err)
	}
	if lib.Len() != 2 {
		t.Fatalf("模板数量 = %d", lib.Len())
	}
	alpha, _ := lib.Get("alpha")
	if alpha.SystemPrompt != "# 甲\n\n正文" || alpha.Autonomy != model.AutonomyAskAll || len(alpha.Tools) != 1 {
		t.Errorf("alpha = %+v", alpha)
	}
	beta, _ := lib.Get("beta")
	if beta.ID != "beta" || beta.Autonomy != model.DefaultAutonomy || beta.SystemPrompt != "" || beta.Tools != nil {
		t.Errorf("缺省字段应取默认值: %+v", beta)
	}
}

func TestLoadTemplatesRejectsInvalid(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"缺少清单":      {"x/SOUL.md": {Data: []byte("p")}},
		"清单不是 JSON": {"x/template.json": {Data: []byte("{")}},
		"未知字段":      {"x/template.json": {Data: []byte(`{"name":"n","typo":1}`)}},
		"id 与目录不一致": {"x/template.json": {Data: []byte(`{"id":"y","name":"n"}`)}},
		"id 含非法字符":  {"Bad_ID/template.json": {Data: []byte(`{"name":"n"}`)}},
		"缺少名称":      {"x/template.json": {Data: []byte(`{"name":" "}`)}},
		"非法自主等级":    {"x/template.json": {Data: []byte(`{"name":"n","autonomy":"nope"}`)}},
		"非法工具名":     {"x/template.json": {Data: []byte(`{"name":"n","tools":["a b"]}`)}},
	}
	for name, fsys := range cases {
		if _, err := LoadTemplates(fsys); err == nil {
			t.Errorf("%s: 应返回错误", name)
		} else if model.CodeOf(err) != model.CodeInvalid {
			t.Errorf("%s: 错误码 = %s，期望 invalid（%v）", name, model.CodeOf(err), err)
		}
	}
	// 空目录是合法的空库。
	lib, err := LoadTemplates(fstest.MapFS{})
	if err != nil || lib.Len() != 0 {
		t.Errorf("空目录应得到空库: %v", err)
	}
}
