package model

import "time"

// OperationFileChangeTool 是人工变更唯一的受控工具名，不向模型声明。
const OperationFileChangeTool = "apply_file_change"

// OperationStatus 是本机用户文件操作的持久状态，与模型 Run 完全分离。
type OperationStatus string

const (
	// OperationCreated 已登记主体，尚未等待用户决定。
	OperationCreated OperationStatus = "created"
	// OperationAwaitingApproval 等待本次预览的一次性审批。
	OperationAwaitingApproval OperationStatus = "awaiting_approval"
	// OperationExecuting 已通过审批，文件副作用正在执行。
	OperationExecuting OperationStatus = "executing"
	// OperationCompleted 已核实文件结果并完成持久化落账。
	OperationCompleted OperationStatus = "completed"
	// OperationFailed 已确认本次操作未成功，不需要继续执行。
	OperationFailed OperationStatus = "failed"
	// OperationCancelled 操作在可取消阶段终止。
	OperationCancelled OperationStatus = "cancelled"
	// OperationExpired 审批或预览过期，不能再次使用原批准执行。
	OperationExpired OperationStatus = "expired"
	// OperationRecoveryRequired 文件结果待核对，继续阻止新的覆盖操作。
	OperationRecoveryRequired OperationStatus = "recovery_required"
)

// Terminal 表示无需再执行或恢复；执行中与需对账状态不能当作已完成。
func (s OperationStatus) Terminal() bool {
	return s == OperationCompleted || s == OperationFailed || s == OperationCancelled || s == OperationExpired
}

// Operation 是真实的本机操作主体，只保留归属和状态，不携带文件正文。
type Operation struct {
	ID          string          `json:"id"`
	OwnerID     string          `json:"owner_id"`
	AgentID     string          `json:"agent_id"`
	SessionID   string          `json:"session_id"`
	ChangeSetID string          `json:"change_set_id"`
	Status      OperationStatus `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// OperationFilter 用于按真实归属列出操作；ActiveOnly 包含需文件状态对账的记录。
type OperationFilter struct {
	OwnerID     string
	AgentID     string
	SessionID   string
	ChangeSetID string
	Status      OperationStatus
	ActiveOnly  bool
}
