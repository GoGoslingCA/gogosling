package app

import (
	"context"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

func Test工作台工具规范采用实际工具且保留用户人格(t *testing.T) {
	for _, locale := range []string{"zh", "en", "fr"} {
		p := workbenchPrompt{skillPrompt{skills: &fakeSkills{}, locale: locale, log: discardLogger()}}
		agent := &model.Agent{ID: "a", SystemPrompt: "user-owned-persona"}
		prompt, err := p.BuildSystemPrompt(context.Background(), agent, nil)
		if err != nil || !strings.HasPrefix(prompt, "user-owned-persona\n") {
			t.Fatalf("必须保留用户人格: %v", err)
		}
		if agent.SystemPrompt != "user-owned-persona" {
			t.Fatal("不能覆盖用户保存的提示")
		}
		for _, tool := range []string{"read_file", "search_files", "edit_file", "expected_sha256", "run_command", "load_skill"} {
			if !strings.Contains(prompt, tool) {
				t.Fatalf("应使用实际工具 %s", tool)
			}
		}
		for _, foreign := range []string{"OtherAssistant", "TaskCreate", "AskUserQuestion", "Claude-Opus", "/private/example-workspace"} {
			if strings.Contains(prompt, foreign) {
				t.Fatalf("不能复制样本的产品身份或不可用工具: %s", foreign)
			}
		}
		if len(i18n.T(locale, "prompt.tool_guidance", nil)) > 2200 {
			t.Fatal("工具规范不应膨胀为整份外部样本")
		}
	}
}
