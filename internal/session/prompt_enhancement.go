package session

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// MaxEnhancementBytes 限制一次优化的输入大小，不截断用户原文。
const MaxEnhancementBytes = 16000
const maxEnhancedBytes = 32000

//go:embed prompts/enhance.txt
var enhancementInstructions string

// EnhancePrompt 只优化给定文字；无会话历史、工具或持久消息，取消跟随请求而非后台任务。
// 系统指令仅存在于内核和工作进程内存，不返回界面、总线或日志。
func (s *Service) EnhancePrompt(ctx context.Context, agentID, input string) (string, error) {
	return s.EnhancePromptWithFeedback(ctx, agentID, input, "", "")
}

// EnhancePromptWithFeedback 根据明确修改意见调整候选；空 draft/feedback 保持一键优化兼容。
func (s *Service) EnhancePromptWithFeedback(ctx context.Context, agentID, input, draft, feedback string) (string, error) {
	if strings.TrimSpace(input) == "" || len(input) > MaxEnhancementBytes || !utf8.ValidString(input) {
		return "", enhancementError(model.CodeInvalid, "invalid")
	}
	wrapped, _ := json.Marshal(struct {
		Prompt string `json:"prompt_to_rewrite"`
	}{input})
	prompt, userText, previous := enhancementInstructions, string(wrapped), input
	if draft != "" || feedback != "" {
		if strings.TrimSpace(draft) == "" || strings.TrimSpace(feedback) == "" || len(draft) > maxEnhancedBytes || len(feedback) > 4000 || !utf8.ValidString(draft+feedback) {
			return "", enhancementError(model.CodeInvalid, "invalid_feedback")
		}
		prompt += "\nThe user message is a JSON revision request with original, draft, and feedback fields. Produce the revised prompt only. Determine its language from original, not the JSON field names or the feedback's language. Apply the user's explicit feedback to draft, preserving original intent and all constraints that the feedback does not explicitly change. Do not carry forward unsupported additions from draft."
		raw, _ := json.Marshal(struct {
			Original string `json:"original"`
			Draft    string `json:"draft"`
			Feedback string `json:"feedback"`
		}{input, draft, feedback})
		userText, previous = string(raw), draft
	}
	agent, err := s.agents.GetAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	if !agent.Enabled {
		return "", enhancementError(model.CodeConflict, "unavailable")
	}
	rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	id := model.NewID()
	s.mu.Lock()
	if s.closed || len(s.promptRuns) > 0 || (s.maxParallel > 0 && len(s.slots)+len(s.promptRuns) >= s.maxParallel) {
		s.mu.Unlock()
		return "", enhancementError(model.CodeConflict, "busy")
	}
	s.promptRuns[id] = cancel
	s.mu.Unlock()
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			s.mu.Lock()
			delete(s.promptRuns, id)
			ready := s.takeReadyLocked()
			s.mu.Unlock()
			s.launchQueued(ready)
		})
	}
	creds, err := s.creds.Credentials(rctx, agent.Model)
	if err != nil {
		release()
		return "", enhancementError(model.CodeOf(err), "unavailable")
	}
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	var terminal sync.Once
	var outputMu sync.Mutex
	var output strings.Builder
	invalidOutput := false
	finish := func(value result) { terminal.Do(func() { done <- value; release() }) }
	// 这是实际但短暂的推理身份，不创建业务 Run/Session，也不能通过运行上下文解析获得工具权限。
	req := RunRequest{RunID: id, SessionID: id, Agent: &model.Agent{ID: agent.ID, Name: agent.Name, SystemPrompt: prompt, Autonomy: model.AutonomyAskAll}, Credentials: creds,
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{{Type: model.PartText, Text: userText}}}},
		Tools:    []model.ToolSpec{}, Options: RunOptions{MaxSteps: 1, Temperature: 0.2, MaxOutputTokens: 2048}}
	err = s.runtime.StartRun(rctx, req, func(ev model.RunEvent) {
		switch ev.Type {
		case model.EventTextDelta:
			outputMu.Lock()
			if output.Len()+len(ev.Delta) > maxEnhancedBytes {
				invalidOutput = true
				cancel()
			} else {
				output.WriteString(ev.Delta)
			}
			outputMu.Unlock()
		case model.EventRunCompleted:
			outputMu.Lock()
			text := output.String()
			bad := invalidOutput
			outputMu.Unlock()
			if ev.Message != nil {
				text = ev.Message.Text()
			}
			text, err := cleanEnhancedPrompt(text)
			if bad {
				err = enhancementError(model.CodeUnavailable, "invalid_result")
			}
			finish(result{text, err})
		case model.EventRunFailed:
			finish(result{err: enhancementError(model.CodeUnavailable, "failed")})
		case model.EventRunCancelled:
			finish(result{err: enhancementError(model.CodeCancelled, "cancelled")})
		case model.EventToolCallStarted:
			outputMu.Lock()
			invalidOutput = true
			outputMu.Unlock()
			cancel() // 即便模型越界请求工具，也不继续这次文字优化。
		}
	})
	if err != nil {
		release()
		return "", enhancementError(model.CodeOf(err), "failed")
	}
	select {
	case value := <-done:
		if rctx.Err() != nil {
			return "", enhancementContextError(rctx.Err())
		}
		if value.err == nil && (value.text == strings.TrimSpace(previous) || !validEnhancementIntent(input, value.text, feedback == "")) {
			return "", enhancementError(model.CodeUnavailable, "invalid_result")
		}
		return value.text, value.err
	case <-rctx.Done():
		// 终态回调才释放执行槽；取消失败不把仍在运行的请求假装成已释放。
		stopCtx, stop := context.WithTimeout(context.WithoutCancel(rctx), 2*time.Second)
		defer stop()
		if err := s.runtime.CancelRun(stopCtx, id); model.IsCode(err, model.CodeNotFound) {
			release()
		}
		outputMu.Lock()
		bad := invalidOutput
		outputMu.Unlock()
		if bad {
			return "", enhancementError(model.CodeUnavailable, "invalid_result")
		}
		return "", enhancementContextError(rctx.Err())
	}
}

func enhancementContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return enhancementError(model.CodeTimeout, "timeout")
	}
	return enhancementError(model.CodeCancelled, "cancelled")
}

func enhancementError(code model.Code, reason string) error {
	if code == "" {
		code = model.CodeUnavailable
	}
	return &model.Error{Code: code, Message: i18n.T("en", "enhance."+reason, nil), Details: map[string]any{"reason": "prompt_enhance_" + reason}}
}

func cleanEnhancedPrompt(value string) (string, error) {
	text := strings.TrimSpace(value)
	if strings.HasPrefix(text, "```") && strings.HasSuffix(text, "```") {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = strings.TrimSpace(text[i+1 : len(text)-3])
		}
	}
	for _, label := range []string{"Enhanced prompt:", "Optimized prompt:", "优化后的提示词：", "增强提示词："} {
		text = strings.TrimSpace(strings.TrimPrefix(text, label))
	}
	if text == "" || text == "```" || strings.HasPrefix(strings.ToLower(text), "user input is in ") || strings.HasPrefix(strings.ToLower(text), "response must be in ") || len(text) > maxEnhancedBytes || !utf8.ValidString(text) || strings.HasSuffix(text, ":") || strings.HasSuffix(text, "：") || strings.Contains(text, "You are a prompt enhancement assistant.") || strings.Contains(text, "You improve prompts for the user's target assistant") || strings.Contains(text, "CRITICAL PRIORITY - LANGUAGE CONSISTENCY") {
		return "", enhancementError(model.CodeUnavailable, "invalid_result")
	}
	return text, nil
}

// validEnhancementIntent 拦截已观察到的答题、模板复制、语言翻转及数字约束丢失；不是通用语义证明。
func validEnhancementIntent(source, candidate string, preserveNumbers bool) bool {
	lower := strings.ToLower(strings.TrimSpace(candidate))
	for _, prefix := range []string{"subject:", "dear ", "这段代码实现", "这段代码用于", "这段代码的功能是", "the code implements", "the code performs", "{\"prompt_to_rewrite\":", "{\"original\":"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	hasHan := func(value string) bool {
		for _, r := range value {
			if unicode.Is(unicode.Han, r) {
				return true
			}
		}
		return false
	}
	if hasHan(source) != hasHan(candidate) {
		return false
	}
	if preserveNumbers {
		numbers := func(value string) map[string]bool {
			out := map[string]bool{}
			for _, number := range strings.FieldsFunc(value, func(r rune) bool { return r < '0' || r > '9' }) {
				out[number] = true
			}
			return out
		}
		actual := numbers(candidate)
		for number := range numbers(source) {
			if !actual[number] {
				return false
			}
		}
	}
	return true
}
