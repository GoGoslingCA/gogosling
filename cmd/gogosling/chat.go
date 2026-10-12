package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 对话循环的时限。
const (
	// chatPollAfter 是某次运行多久没有事件后改用轮询运行记录兜底（事件流断开、订阅晚于运行开始等）。
	chatPollAfter = 1500 * time.Millisecond
	// chatPollInterval 是兜底轮询的间隔。
	chatPollInterval = time.Second
	// maxArgsSummary / maxResultSummary 是工具调用一行摘要的字符上限。
	maxArgsSummary   = 80
	maxResultSummary = 100
)

// errQuit 表示用户在运行中第二次按下 Ctrl-C，要求退出。
var errQuit = errors.New("quit")

// ANSI 样式（只在 cli.color 为 true 时使用）。
const (
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

func (c *cli) cmdChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	sessionID := fs.String("session", "", "")
	once := fs.String("once", "", "")
	pos, err := c.parse(fs, args)
	if err != nil {
		return err
	}
	if err := c.need(pos, "<agent>"); err != nil {
		return err
	}
	cl, err := c.connect(ctx)
	if err != nil {
		return err
	}
	agent, err := c.resolveAgent(ctx, cl, pos[0])
	if err != nil {
		return err
	}
	var sess sessionView
	if *sessionID != "" {
		sess, err = cl.GetSession(ctx, *sessionID)
	} else {
		sess, err = cl.CreateSession(ctx, agent.ID, "")
	}
	if err != nil {
		return err
	}

	// 先订阅事件流再发送：运行的事件不会早于订阅发布而丢失（兜底轮询处理订阅建立得晚的情况）。
	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	events, err := cl.Events(streamCtx, sess.ID)
	if err != nil {
		return err
	}
	sigs, stopSigs := c.interrupts()
	defer stopSigs()

	t := &chatTurn{c: c, cl: cl, events: events, sigs: sigs}
	if strings.TrimSpace(*once) != "" {
		status, err := t.run(ctx, sess.ID, *once)
		if err != nil {
			if errors.Is(err, errQuit) {
				return &silentError{code: exitInterrupts}
			}
			return err
		}
		if status != model.RunCompleted {
			return &silentError{code: exitFailure}
		}
		return nil
	}

	c.println(c.t("chat_header", map[string]any{"agent": agent.Name, "session": sess.ID}))
	for {
		c.printf("%s", c.t("chat_prompt", nil))
		line, interrupted, err := c.nextInput(ctx, sigs)
		if interrupted || errors.Is(err, io.EOF) {
			c.println("")
			c.println(c.t("bye", nil))
			return nil
		}
		if err != nil {
			return err
		}
		text := strings.TrimSpace(line)
		switch text {
		case "":
			continue
		case "/exit", "/quit":
			c.println(c.t("bye", nil))
			return nil
		}
		if _, err := t.run(ctx, sess.ID, text); err != nil {
			if errors.Is(err, errQuit) {
				c.println(c.t("bye", nil))
				return nil
			}
			if ctx.Err() != nil {
				return err
			}
			fmt.Fprintln(c.errOut, c.t("error", map[string]any{"message": describeError(err)}))
		}
	}
}

// nextInput 等待下一行输入或 Ctrl-C。
func (c *cli) nextInput(ctx context.Context, sigs <-chan os.Signal) (string, bool, error) {
	if c.lines == nil {
		c.lines = newLineReader(c.stdin)
	}
	c.lines.start()
	select {
	case res, ok := <-c.lines.ch:
		if !ok {
			return "", false, io.EOF
		}
		return res.line, false, res.err
	case <-sigs:
		return "", true, nil
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
}

// chatTurn 渲染一次次运行的事件流。
type chatTurn struct {
	c      *cli
	cl     api
	events <-chan streamEvent
	sigs   <-chan os.Signal

	// 当前运行的渲染状态。
	runID     string
	lineStart bool // 光标是否在行首
	reasoning bool // 正在输出思考内容
	gotText   bool // 是否已经流式输出过正文
	asked     map[string]bool
	calls     map[string]string // call_id → 工具名，用于 tool.call.completed 的摘要
	streamOK  bool
	// sawEvent 表示已经收到过本运行的事件；此前的 gap 只说明订阅起点早于回放缓冲，不提示用户。
	sawEvent bool
}

// run 发送一条消息并渲染该运行直到终态，返回终态。
func (t *chatTurn) run(ctx context.Context, sessionID, text string) (model.RunStatus, error) {
	c := t.c
	run, err := t.cl.SendMessage(ctx, sessionID, text)
	if err != nil {
		if model.IsCode(err, model.CodeConflict) {
			return "", errors.New(c.t("busy", nil))
		}
		return "", err
	}
	t.runID, t.lineStart, t.reasoning, t.gotText = run.ID, true, false, false
	t.asked = map[string]bool{}
	t.calls = map[string]string{}
	t.streamOK = t.events != nil
	t.sawEvent = false
	if run.Status.Terminal() {
		return t.finishFromRecord(ctx, run)
	}

	interrupted := false
	lastEvent := time.Now()
	poll := time.NewTicker(chatPollInterval)
	defer poll.Stop()
	events := t.events
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				t.noteStreamLost()
				continue
			}
			if ev.Type == "gap" {
				if t.sawEvent {
					t.noteStreamLost()
				}
				continue
			}
			if ev.RunID != t.runID {
				continue
			}
			t.sawEvent = true
			lastEvent = time.Now()
			status, done, err := t.handle(ctx, ev, &interrupted)
			if err != nil || done {
				return status, err
			}
		case <-t.sigs:
			if interrupted {
				t.newline()
				return "", errQuit
			}
			interrupted = true
			t.newline()
			c.println(c.t("cancelling", nil))
			if _, err := t.cl.CancelRun(ctx, t.runID); err != nil {
				fmt.Fprintln(c.errOut, c.t("error", map[string]any{"message": describeError(err)}))
			}
		case <-poll.C:
			if time.Since(lastEvent) < chatPollAfter {
				continue
			}
			cur, err := t.cl.GetRun(ctx, t.runID)
			if err != nil {
				continue
			}
			if cur.Status.Terminal() {
				return t.finishFromRecord(ctx, cur)
			}
			// 一段时间没有事件且运行未结束：可能错过了 approval.requested（订阅晚于事件，或运行状态
			// 没有反映审批等待），从待审批列表里补问本运行的请求。
			if err := t.askPending(ctx, &interrupted); err != nil {
				return "", err
			}
			lastEvent = time.Now()
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// handle 渲染一条本运行的事件；done 为 true 表示运行已到终态。
func (t *chatTurn) handle(ctx context.Context, ev streamEvent, interrupted *bool) (model.RunStatus, bool, error) {
	c := t.c
	re, ok := ev.runEvent()
	if !ok {
		return "", false, nil
	}
	switch re.Type {
	case model.EventTextDelta:
		t.endReasoning()
		t.write(re.Delta)
		t.gotText = t.gotText || re.Delta != ""
	case model.EventReasoningDelta:
		if !t.reasoning {
			t.newline()
			t.reasoning = true
			t.write(t.dim(c.t("reasoning_prefix", nil)))
		}
		t.write(t.dim(strings.ReplaceAll(re.Delta, "\n", "\n"+c.t("reasoning_prefix", nil))))
	case model.EventToolCallStarted:
		t.endReasoning()
		t.newline()
		if re.ToolCall != nil {
			t.calls[re.ToolCall.ID] = re.ToolCall.Name
			c.println(c.t("tool_started", map[string]any{"tool": re.ToolCall.Name, "args": clipText(compactJSON(re.ToolCall.Arguments), maxArgsSummary)}))
		}
	case model.EventToolCallCompleted:
		t.newline()
		if re.ToolResult != nil {
			key := "tool_completed"
			if re.ToolResult.IsError {
				key = "tool_failed"
			}
			name := t.calls[re.ToolResult.CallID]
			if name == "" {
				name = re.ToolResult.CallID
			}
			c.println(c.t(key, map[string]any{"tool": name, "result": clipText(re.ToolResult.Content, maxResultSummary)}))
		}
	case model.EventApprovalRequested:
		if re.Approval != nil && re.Approval.Status == model.ApprovalPending {
			if err := t.ask(ctx, *re.Approval, interrupted); err != nil {
				return "", false, err
			}
		}
	case model.EventRunCompleted:
		t.endReasoning()
		t.newline()
		usage := model.Usage{}
		if re.Usage != nil {
			usage = *re.Usage
		}
		if !t.gotText && re.Message != nil {
			// 运行没有流式输出正文（例如订阅建立得晚）：用最终消息补上。
			if txt := re.Message.Text(); txt != "" {
				c.println(txt)
			}
		}
		c.println(c.t("run_done", map[string]any{"input": usage.InputTokens, "output": usage.OutputTokens}))
		return model.RunCompleted, true, nil
	case model.EventRunFailed:
		t.endReasoning()
		t.newline()
		fmt.Fprintln(c.errOut, c.t("run_failed", map[string]any{"error": re.Error}))
		return model.RunFailed, true, nil
	case model.EventRunCancelled:
		t.endReasoning()
		t.newline()
		c.println(c.t("run_cancelled", nil))
		return model.RunCancelled, true, nil
	}
	return "", false, nil
}

// ask 询问用户是否批准，并提交决定。Ctrl-C 取消整个运行（审批随之过期）；输入结束视为拒绝。
func (t *chatTurn) ask(ctx context.Context, ar model.ApprovalRequest, interrupted *bool) error {
	c := t.c
	if t.asked[ar.ID] {
		return nil
	}
	t.asked[ar.ID] = true
	t.endReasoning()
	t.newline()
	c.println(c.t("approval_requested", map[string]any{"summary": ar.Summary, "risk": string(ar.Risk)}))
	var dec model.Decision
	for {
		c.printf("%s ", c.t("approval_prompt", map[string]any{"tool": ar.ToolCall.Name}))
		line, sig, err := c.nextInput(ctx, t.sigs)
		if sig {
			*interrupted = true
			c.println("")
			c.println(c.t("cancelling", nil))
			_, cerr := t.cl.CancelRun(ctx, t.runID)
			return cerr
		}
		if errors.Is(err, io.EOF) {
			c.println("")
			dec = model.Decision{Approve: false, Scope: model.ScopeOnce}
			break
		}
		if err != nil {
			return err
		}
		choice := strings.ToLower(strings.TrimSpace(line))
		if choice == "y" || choice == "yes" {
			dec = model.Decision{Approve: true, Scope: model.ScopeOnce}
		} else if choice == "s" || choice == "session" {
			dec = model.Decision{Approve: true, Scope: model.ScopeSession}
		} else if choice == "n" || choice == "no" {
			dec = model.Decision{Approve: false, Scope: model.ScopeOnce}
		} else {
			c.println(c.t("approval_choice_invalid", nil))
			continue
		}
		break
	}
	if _, err := t.cl.DecideApproval(ctx, ar.ID, dec); err != nil {
		if model.IsCode(err, model.CodeConflict) {
			c.println(c.t("approval_already", nil))
			return nil
		}
		return err
	}
	if dec.Approve {
		c.println(c.t("approval_approved", nil))
	} else {
		c.println(c.t("approval_denied", nil))
	}
	t.lineStart = true
	return nil
}

// askPending 从待审批列表里找到本运行的请求并询问（兜底：错过了 approval.requested 事件）。
func (t *chatTurn) askPending(ctx context.Context, interrupted *bool) error {
	list, err := t.cl.ListApprovals(ctx, string(model.ApprovalPending))
	if err != nil {
		return nil
	}
	for _, ar := range list {
		if ar.RunID == t.runID {
			if err := t.ask(ctx, ar, interrupted); err != nil {
				return err
			}
		}
	}
	return nil
}

// finishFromRecord 在没有收到终态事件时按运行记录收尾。
func (t *chatTurn) finishFromRecord(ctx context.Context, run model.Run) (model.RunStatus, error) {
	c := t.c
	t.endReasoning()
	t.newline()
	switch run.Status {
	case model.RunCompleted:
		c.println(c.t("run_done", map[string]any{"input": run.Usage.InputTokens, "output": run.Usage.OutputTokens}))
	case model.RunFailed:
		fmt.Fprintln(c.errOut, c.t("run_failed", map[string]any{"error": run.Error}))
	case model.RunCancelled:
		c.println(c.t("run_cancelled", nil))
	}
	return run.Status, nil
}

func (t *chatTurn) noteStreamLost() {
	if t.streamOK {
		t.streamOK = false
		t.newline()
		fmt.Fprintln(t.c.errOut, t.c.t("stream_lost", nil))
	}
}

// write 输出文本并记录光标是否在行首。
func (t *chatTurn) write(s string) {
	if s == "" {
		return
	}
	fmt.Fprint(t.c.out, s)
	t.lineStart = strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\n"+ansiReset)
}

// newline 在光标不在行首时换行。
func (t *chatTurn) newline() {
	if !t.lineStart {
		fmt.Fprintln(t.c.out)
		t.lineStart = true
	}
}

// endReasoning 结束思考内容段落。
func (t *chatTurn) endReasoning() {
	if t.reasoning {
		t.reasoning = false
		t.newline()
	}
}

// dim 在启用颜色时把文本包成暗色。
func (t *chatTurn) dim(s string) string {
	if !t.c.color || s == "" {
		return s
	}
	return ansiDim + s + ansiReset
}

// compactJSON 把工具参数压成一行 JSON；不是合法 JSON 时原样返回。
func compactJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
