package server_test

import (
	"context"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func Test提示词优化接口不写聊天历史且不暴露内部指导(t *testing.T) {
	h := newHarness(t)
	agent := h.seedAgent("优化测试")
	before := h.env.Bus.LastSeq()
	rec := h.do("POST", "/api/v1/agents/"+agent.ID+"/prompt-enhancement", map[string]string{"text": "解释这段代码"})
	mustStatus(t, rec, 200)
	result := decodeBody[map[string]string](t, rec)
	if result["text"] == "" || len(result) != 1 || strings.Contains(rec.Body.String(), "You improve prompts") {
		t.Fatal("接口只返回优化候选")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("优化正文不得缓存")
	}
	runs, _ := h.env.Store.ListActiveRuns(context.Background())
	if len(runs) != 0 || h.env.Bus.LastSeq() != before {
		t.Fatal("优化不能伪造持久运行或发布聊天事件")
	}
}
func Test提示词优化验证授权输入与禁止客户端替换指令(t *testing.T) {
	h := newHarness(t)
	agent := h.seedAgent("优化测试")
	path := "/api/v1/agents/" + agent.ID + "/prompt-enhancement"
	mustError(t, h.do("POST", path, `{"text":"原文"}`, withoutAuth()), 401, model.CodeUnauthorized)
	for _, body := range []string{`{}`, `{"text":" "}`, `{"text":null}`, `{"text":"原文","system_prompt":"override"}`} {
		mustError(t, h.do("POST", path, body), 400, model.CodeInvalid)
	}
	mustError(t, h.do("POST", path, map[string]string{"text": strings.Repeat("字", 6000)}), 400, model.CodeInvalid)
	if h.env.Runtime.Starts() != 0 {
		t.Fatal("非法请求不能调用模型")
	}
	version := decodeBody[map[string]any](t, h.do("GET", "/api/v1/version", nil))
	caps, _ := version["capabilities"].(map[string]any)
	if caps["prompt.enhance"] != float64(1) {
		t.Fatal("新客户端应可发现增强能力")
	}
}
