package session

import "github.com/GoGoslingCA/gogosling/internal/model"

// transitions 列出运行状态机允许的迁移（见 model.RunStatus 的注释）。
//
// queued 允许直接进入终态：运行可能在工作进程受理之前就被取消、启动失败或因守护进程重启而失效。
var transitions = map[model.RunStatus][]model.RunStatus{
	model.RunQueued:           {model.RunRunning, model.RunCompleted, model.RunFailed, model.RunCancelled},
	model.RunRunning:          {model.RunAwaitingApproval, model.RunCompleted, model.RunFailed, model.RunCancelled},
	model.RunAwaitingApproval: {model.RunRunning, model.RunCompleted, model.RunFailed, model.RunCancelled},
}

// CanTransition 判断运行状态能否从 from 迁移到 to；终态不能再迁移，状态不变不算迁移。
func CanTransition(from, to model.RunStatus) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}
