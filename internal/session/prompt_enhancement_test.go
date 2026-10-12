package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func Test提示词优化只改写文字且不产生会话和事件(t *testing.T) {
	for _, tc := range []struct{ in, out string }{{"请解释这段代码", "请解释这段代码的功能、关键流程与边界情况。"}, {"Explain this code", "Explain this code's purpose, control flow, and relevant edge cases."}, {"代码有 bug, help me fix it", "请定位代码中的 bug, explain its cause, and suggest a minimal verified fix."}} {
		t.Run(tc.in, func(t *testing.T) {
			h := newHarness(t, Options{MaxParallelRuns: 2})
			before := h.bus.LastSeq()
			h.rt.onStart = func(req RunRequest, sink func(model.RunEvent)) {
				var wrapped struct {
					Prompt string `json:"prompt_to_rewrite"`
				}
				_ = json.Unmarshal([]byte(req.Messages[0].Text()), &wrapped)
				if len(req.Tools) != 0 || len(req.Messages) != 1 || wrapped.Prompt != tc.in || req.Options.MaxSteps != 1 {
					t.Error("优化只能有原文和一次无工具推理")
				}
				if req.Agent.SystemPrompt == h.agent.SystemPrompt || !strings.Contains(req.Agent.SystemPrompt, "mixed-language") {
					t.Error("应隔离用户人格并保持混合语言约束")
				}
				sink(model.RunEvent{Type: model.EventRunCompleted, Message: &model.Message{Role: model.RoleAssistant, Parts: []model.Part{{Type: model.PartText, Text: tc.out}}}})
			}
			got, err := h.svc.EnhancePrompt(context.Background(), h.agent.ID, tc.in)
			if err != nil || got != tc.out {
				t.Fatalf("候选应保持源语言：%v", err)
			}
			if h.bus.LastSeq() != before || h.svc.Resources().Running != 0 {
				t.Fatal("不应发布聊天事件或遗留执行槽")
			}
			sessions, _ := h.svc.List(context.Background(), model.SessionFilter{}, model.Page{})
			if len(sessions) != 0 {
				t.Fatal("不得创建业务会话")
			}
		})
	}
}
func Test优化拒绝空白超长停用与重复调用(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 1})
	ctx := context.Background()
	for _, value := range []string{" ", strings.Repeat("字", MaxEnhancementBytes), string([]byte{0xff})} {
		if _, err := h.svc.EnhancePrompt(ctx, h.agent.ID, value); !model.IsCode(err, model.CodeInvalid) {
			t.Fatal("无效输入须先拒绝")
		}
	}
	if h.rt.requestCount() != 0 {
		t.Fatal("无效输入不能调用模型")
	}
	h.agent.Enabled = false
	h.agents.set(h.agent)
	if _, err := h.svc.EnhancePrompt(ctx, h.agent.ID, "原文"); !model.IsCode(err, model.CodeConflict) {
		t.Fatal("停用助手不可优化")
	}
	h.agent.Enabled = true
	h.agents.set(h.agent)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := h.svc.EnhancePrompt(rctx, h.agent.ID, "原文"); done <- err }()
	waitForDispatch(t, h, 1)
	if _, err := h.svc.EnhancePrompt(ctx, h.agent.ID, "重复"); !model.IsCode(err, model.CodeConflict) {
		t.Fatal("全局不得并发优化")
	}
	queued := h.send(h.session().ID, "等待正常任务")
	if h.rt.requestCount() != 1 {
		t.Fatal("优化也应计入并发预算")
	}
	cancel()
	select {
	case err := <-done:
		if !model.IsCode(err, model.CodeCancelled) {
			t.Fatal("取消应保留取消状态")
		}
	case <-time.After(time.Second):
		t.Fatal("取消未收敛")
	}
	waitForDispatch(t, h, 2)
	if h.rt.lastRequest(t).RunID != queued.ID {
		t.Fatal("释放优化槽后应派发正常任务")
	}
}
func Test优化失败不暴露上游正文并释放资源(t *testing.T) {
	h := newHarness(t, Options{MaxParallelRuns: 2})
	h.rt.onStart = func(req RunRequest, sink func(model.RunEvent)) {
		sink(model.RunEvent{Type: model.EventRunFailed, Error: "upstream-private-body"})
	}
	_, err := h.svc.EnhancePrompt(context.Background(), h.agent.ID, "请优化")
	if err == nil || strings.Contains(err.Error(), "upstream-private-body") || h.svc.Resources().Running != 0 {
		t.Fatal("上游错误不得泄漏，失败须释放槽")
	}
	h.rt.startErr = errors.New("private-start-error")
	_, err = h.svc.EnhancePrompt(context.Background(), h.agent.ID, "请优化")
	if err == nil || strings.Contains(err.Error(), "private-start-error") || h.svc.Resources().Running != 0 {
		t.Fatal("启动错误也须脱敏与释放")
	}
}
func Test优化截止与关闭会取消推理(t *testing.T) {
	for _, stop := range []string{"timeout", "close"} {
		t.Run(stop, func(t *testing.T) {
			h := newHarness(t, Options{MaxParallelRuns: 2})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := h.svc.EnhancePrompt(ctx, h.agent.ID, "原文"); done <- err }()
			waitForDispatch(t, h, 1)
			if stop == "close" {
				h.svc.Close()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("中断不能返回成功")
				}
			case <-time.After(time.Second):
				t.Fatal("中断未收敛")
			}
			if h.rt.cancelCount() != 1 || h.svc.Resources().Running != 0 {
				t.Fatal("中断应取消真实推理并释放槽")
			}
		})
	}
}
func Test优化输出拒绝指令泄漏与未完成内容(t *testing.T) {
	for _, text := range []string{"", "```", "下一步：", enhancementInstructions, "User input is in Chinese → Response must be in Chinese."} {
		if _, err := cleanEnhancedPrompt(text); err == nil {
			t.Fatal("不能返回内部指令、分析标签或不完整文本")
		}
	}
	for _, text := range []string{"Enhanced prompt: Explain the control flow.", "```text\nExplain the control flow.\n```"} {
		got, err := cleanEnhancedPrompt(text)
		if err != nil || got != "Explain the control flow." {
			t.Fatal("应去掉意外外层标签和围栏")
		}
	}
}

func Test优化越界工具或过大输出会取消且不返回候选(t *testing.T) {
	for _, kind := range []string{"tool", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, Options{MaxParallelRuns: 2})
			h.rt.onStart = func(req RunRequest, sink func(model.RunEvent)) {
				if kind == "tool" {
					sink(model.RunEvent{Type: model.EventToolCallStarted})
				} else {
					sink(model.RunEvent{Type: model.EventTextDelta, Delta: strings.Repeat("x", maxEnhancedBytes+1)})
				}
			}
			text, err := h.svc.EnhancePrompt(context.Background(), h.agent.ID, "解释代码")
			if err == nil || text != "" || h.rt.cancelCount() != 1 || h.svc.Resources().Running != 0 {
				t.Fatal("越界优化须中止并丢弃候选")
			}
		})
	}
}
func Test仅增量终态也能形成完整候选且输入缩进保留(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.onStart = func(req RunRequest, sink func(model.RunEvent)) {
		var wrapped struct {
			Prompt string `json:"prompt_to_rewrite"`
		}
		_ = json.Unmarshal([]byte(req.Messages[0].Text()), &wrapped)
		if wrapped.Prompt != "  explain code\n" {
			t.Fatal("源输入空白不应在传递前被改变")
		}
		sink(model.RunEvent{Type: model.EventTextDelta, Delta: "Explain the code's "})
		sink(model.RunEvent{Type: model.EventTextDelta, Delta: "control flow."})
		sink(model.RunEvent{Type: model.EventRunCompleted})
	}
	text, err := h.svc.EnhancePrompt(context.Background(), h.agent.ID, "  explain code\n")
	if err != nil || text != "Explain the code's control flow." {
		t.Fatalf("应合并完整增量：%v", err)
	}
}

func Test优化拦截真实模型曾出现的代答和约束丢失(t *testing.T) {
	cases := []struct{ source, candidate string }{
		{"请解释代码，200字以内。", "这段代码实现了对输入文本进行重写的功能。"},
		{"Rewrite this email, keeping all names unchanged.", "Subject: Project delivery\nDear team, ..."},
		{"Explain this code under 120 words.", "请解释这段代码的功能。"},
		{"最多6条，不虚构日期。", "请整理一份清晰完整的清单。"},
	}
	for _, tc := range cases {
		if validEnhancementIntent(tc.source, tc.candidate, true) {
			t.Fatal("已知错误候选不能自动覆盖输入")
		}
	}
}
func Test修改意见独立传入并允许显式调整原数字约束(t *testing.T) {
	h := newHarness(t, Options{})
	h.rt.onStart = func(req RunRequest, sink func(model.RunEvent)) {
		var input map[string]string
		_ = json.Unmarshal([]byte(req.Messages[0].Text()), &input)
		if input["original"] != "请用200字解释代码。" || input["draft"] != "请在200字内介绍代码功能。" || input["feedback"] != "改成100字，不要新增内容。" {
			t.Fatal("原始意图、当前候选和意见必须独立传递")
		}
		sink(model.RunEvent{Type: model.EventRunCompleted, Message: &model.Message{Role: model.RoleAssistant, Parts: []model.Part{{Type: model.PartText, Text: "请在100字以内解释代码功能，不新增内容。"}}}})
	}
	value, err := h.svc.EnhancePromptWithFeedback(context.Background(), h.agent.ID, "请用200字解释代码。", "请在200字内介绍代码功能。", "改成100字，不要新增内容。")
	if err != nil || value == "" {
		t.Fatalf("显式意见应能更新约束：%v", err)
	}
	for _, pair := range [][2]string{{"", "简短"}, {"候选", ""}, {"候选", strings.Repeat("字", 2000)}} {
		if _, err := h.svc.EnhancePromptWithFeedback(context.Background(), h.agent.ID, "原文", pair[0], pair[1]); !model.IsCode(err, model.CodeInvalid) {
			t.Fatal("不完整或超长意见必须拒绝")
		}
	}
}
