package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
)

// ConditionalTextLimit 是条件文件操作允许的最大 UTF-8 正文字节数。
const ConditionalTextLimit int64 = 512 << 10

const reservationPrefix = ".gogosling-reservation-"

// Snapshot 绑定工作区、父目录、文件身份与内容；Identity 是本包的不可解释版本串。
// Content 由调用方保存到受保护的内核仓储，不应写日志或事件。
type Snapshot struct {
	Exists       bool   `json:"exists"`
	SHA256       string `json:"sha256"`
	Identity     string `json:"identity"`
	RootIdentity string `json:"root_identity"`
	Content      string `json:"-"`
	Size         int64  `json:"size"`
}

// Receipt 描述真实文件结果；错误时也必须检查 State，不可把任意错误当作未发生副作用。
// State 为 unchanged、conflict、restored、applied 或 recovery_required。
type Receipt struct {
	OperationID string   `json:"operation_id"`
	Path        string   `json:"path"`
	State       string   `json:"state"`
	Before      Snapshot `json:"before"`
	// After.Identity 为空表示结果未知，不能把零值 Exists=false 当成目标已删除。
	After  Snapshot `json:"after"`
	Reason string   `json:"reason,omitempty"`
}

// conditionalHooks 仅供包内测试在精确竞态点注入外部文件变化，不使用全局钩子。
type conditionalHooks struct{ beforeMove, beforeInstall, afterInstall func() }

// Snapshot 严格读取工作区内的单个普通文本文件或缺失目标，并冻结父目录身份。
// 拒绝符号链接、目录、硬链接、特殊权限、ACL及跨文件系统目录；不支持的平台返回 unavailable。
func (w *Workspace) Snapshot(ctx context.Context, rel string, maxBytes int64) (Snapshot, error) {
	if err := w.lockWrite(ctx); err != nil {
		return Snapshot{}, err
	}
	defer w.unlockWrite()
	if err := strictConditionalPath(rel); err != nil {
		return Snapshot{}, err
	}
	if maxBytes <= 0 || maxBytes > ConditionalTextLimit {
		maxBytes = ConditionalTextLimit
	}
	return w.snapshotConditional(ctx, rel, maxBytes)
}

// Apply 在冻结快照仍匹配时执行一次条件操作；nil 正文表示受控删除。
// 先保留原条目，再验证被移动对象；安装和恢复均禁止覆盖同时出现的外部文件。
// 本方法不等同对任意外部写者的原子 CAS：持有旧描述符原地写入或移动父目录仍需恢复处理。
func (w *Workspace) Apply(ctx context.Context, operationID, rel string, expected Snapshot, proposed *string) (Receipt, error) {
	r := Receipt{OperationID: operationID, Path: rel, State: "unchanged", Before: expected}
	if err := validateConditionalInput(operationID, rel, expected, proposed); err != nil {
		return r, err
	}
	if err := w.lockWrite(ctx); err != nil {
		return r, err
	}
	defer w.unlockWrite()
	return w.applyConditional(ctx, r, proposed)
}

// Recover 使用内核提供的冻结快照和提议检查持久保留记录，不重新执行新操作。
// 缺少保留记录返回 not_found，调用方须结合自身数据库判断，不能仅凭正文摘要猜测成功。
func (w *Workspace) Recover(ctx context.Context, operationID, rel string, expected Snapshot, proposed *string) (Receipt, error) {
	r := Receipt{OperationID: operationID, Path: rel, State: "recovery_required", Before: expected}
	if err := validateConditionalInput(operationID, rel, expected, proposed); err != nil {
		return r, err
	}
	if err := w.lockWrite(ctx); err != nil {
		return r, err
	}
	defer w.unlockWrite()
	return w.recoverConditional(ctx, r, proposed)
}

// Finalize 只能在调用方已持久记录 applied/restored 结果后调用；释放未变化的保留原件。
// 不确定状态、原件被外部写者改变或保留目录被替换时拒绝清理。
func (w *Workspace) Finalize(ctx context.Context, operationID, rel string) error {
	if err := strictConditionalPath(rel); err != nil {
		return err
	}
	if operationID == "" || len(operationID) > 128 {
		return conditionalError(model.CodeInvalid, "invalid_operation", nil)
	}
	if err := w.lockWrite(ctx); err != nil {
		return err
	}
	defer w.unlockWrite()
	return w.finalizeConditional(ctx, operationID, rel)
}

func strictConditionalPath(rel string) error {
	if rel == "" || rel == "." || path.Clean(rel) != rel || strings.ContainsAny(rel, "\\:") || strings.HasPrefix(rel, "/") || hasDotDot(rel) || reservedPath(rel) {
		return conditionalError(model.CodeForbidden, "unsafe_path", nil)
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return conditionalError(model.CodeInvalid, "unsafe_path", nil)
		}
	}
	return nil
}

func validateConditionalInput(id, rel string, expected Snapshot, proposed *string) error {
	if err := strictConditionalPath(rel); err != nil {
		return err
	}
	if id == "" || len(id) > 128 || expected.Identity == "" || expected.RootIdentity == "" {
		return conditionalError(model.CodeInvalid, "invalid_snapshot", nil)
	}
	if expected.Size < 0 || expected.Size > ConditionalTextLimit || !utf8.ValidString(expected.Content) || int64(len(expected.Content)) != expected.Size {
		return conditionalError(model.CodeInvalid, "invalid_snapshot", nil)
	}
	if expected.Exists {
		if digestText(expected.Content) != expected.SHA256 {
			return conditionalError(model.CodeInvalid, "invalid_snapshot", nil)
		}
	} else if expected.Content != "" || expected.Size != 0 || expected.SHA256 != "" {
		return conditionalError(model.CodeInvalid, "invalid_snapshot", nil)
	}
	if proposed == nil && !expected.Exists {
		return conditionalError(model.CodeInvalid, "missing_delete_target", nil)
	}
	if proposed != nil && (!utf8.ValidString(*proposed) || int64(len(*proposed)) > ConditionalTextLimit) {
		return conditionalError(model.CodeInvalid, "invalid_text", nil)
	}
	return nil
}

func digestText(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
func reservationName(id string) string {
	return reservationPrefix + fmt.Sprintf("%x", sha256.Sum256([]byte(id)))[:32]
}
func reservedPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(strings.ReplaceAll(rel, "\\", "/")), "/") {
		if strings.HasPrefix(strings.ToLower(part), reservationPrefix) {
			return true
		}
	}
	return false
}
func conditionalError(code model.Code, reason string, err error) error {
	return &model.Error{Code: code, Message: i18n.T("en", "workspace."+reason, nil), Details: map[string]any{"reason": reason}, Err: err}
}

func (w *Workspace) lockWrite(ctx context.Context) error {
	if err := checkCtx(ctx); err != nil {
		return err
	}
	if w.writeGate == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return conditionalError(model.CodeCancelled, "cancelled", ctx.Err())
	case <-w.writeGate:
		return nil
	}
}
func (w *Workspace) unlockWrite() {
	if w.writeGate != nil {
		w.writeGate <- struct{}{}
	}
}

func rejectReservationTree(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := checkCtx(ctx); err != nil {
			return err
		}
		if reservedPath(d.Name()) {
			return conditionalError(model.CodeConflict, "pending_file_operation", nil)
		}
		return nil
	})
}
