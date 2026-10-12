package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// MetaPushed 为 true 表示该助手消息由 Push 直接写入（如 push_text 定时任务），没有经过模型。
const MetaPushed = "pushed"

// Push 不经模型直接向会话追加一条助手消息，并发布 message.created 与 session.updated，
// 供 push_text 定时任务等主动推送使用。返回写入的消息。
//
// 约束：文本不能为空（CodeInvalid）；会话必须是 active（否则 CodeConflict）；
// 同会话有在途运行时返回 CodeConflict（Details 含 run_id）——推送若插进一次运行的
// 用户消息与回复之间，会打乱下一次运行的历史顺序。服务关闭后返回 CodeUnavailable。
//
// 推送消息的 RunID 为空，Meta 标记 pushed=true；它会作为普通助手消息进入后续运行的历史。
func (s *Service) Push(ctx context.Context, sessionID, text string) (*model.Message, error) {
	if sessionID == "" {
		return nil, model.NewError(model.CodeInvalid, "缺少会话 ID")
	}
	if strings.TrimSpace(text) == "" {
		return nil, model.NewError(model.CodeInvalid, "推送内容为空")
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, model.NewError(model.CodeUnavailable, "会话服务已关闭")
	}

	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status != model.SessionActive {
		return nil, &model.Error{
			Code:    model.CodeConflict,
			Message: "会话已归档",
			Details: map[string]any{"session_id": sess.ID},
		}
	}
	// 检查与写入之间仍可能有 Send 挤入：那样推送会落在新用户消息之后、回复之前，
	// 只影响该次运行之后的历史顺序，不破坏任何不变量，因此不为此额外占位。
	if runID, busy := s.ActiveRunID(sessionID); busy {
		return nil, &model.Error{
			Code:    model.CodeConflict,
			Message: "会话仍有未结束的运行，稍后再推送",
			Details: map[string]any{"run_id": runID},
		}
	}

	now := s.now()
	msg := &model.Message{
		ID:        model.NewID(),
		SessionID: sess.ID,
		Role:      model.RoleAssistant,
		Parts:     []model.Part{{Type: model.PartText, Text: text}},
		Meta:      mustJSON(map[string]any{MetaPushed: true}),
		CreatedAt: now,
	}
	if err := s.store.AppendMessage(ctx, msg); err != nil {
		return nil, fmt.Errorf("追加推送消息: %w", err)
	}
	s.publishRun(model.RunEvent{
		Type: model.EventMessageCreated, SessionID: sess.ID, AgentID: sess.AgentID, At: now, Message: msg,
	})
	// AppendMessage 刷新了 last_message_at；重新读取后发布，界面据此调整会话排序。
	if updated, err := s.store.GetSession(ctx, sess.ID); err == nil {
		s.publishSession(EventSessionUpdated, updated)
	} else {
		s.log.Warn("推送后读取会话失败", "session_id", sess.ID, "error", err)
	}
	return msg, nil
}
