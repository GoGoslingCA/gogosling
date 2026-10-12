package scheduler

import (
	"context"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件定义 scheduler 包对外部能力的最小依赖。它们都是本包自己的接口，
// 集成阶段由 internal/app 组合根用 session 等包的实现来适配；本包不导入那些包。

// Runner 执行一次定时任务（期望由 session 层适配）。
//
// 实现方根据 job.Kind 决定动作：push_text 把 job.Input 作为消息直接写入目标会话；
// run_agent 以 job.Input 为用户输入发起一次运行并等待其结束。job.SessionKey 为空时
// 由实现方解析为该智能体的默认桌面会话。
// 返回的错误会写入 Job.LastError（只取 *model.Error 的 Message），因此不得包含凭据。
// ctx 被取消（守护进程关闭或超过 Options.RunTimeout）时实现方应尽力中止。
// 本包保证同一任务的 RunJob 不会并发调用；不同任务可能并发。
type Runner interface {
	RunJob(ctx context.Context, job *model.Job) error
}

// Clock 是调度循环的时间来源，便于测试时用假时钟驱动。
type Clock interface {
	// Now 返回当前时间；实现可返回任意时区，本包内部统一转为 UTC。
	Now() time.Time
	// After 在 d 之后向返回的通道投递一次当前时间，语义同 time.After。
	After(d time.Duration) <-chan time.Time
}

// SystemClock 用真实的系统时间实现 Clock。
type SystemClock struct{}

// Now 返回 time.Now()。
func (SystemClock) Now() time.Time { return time.Now() }

// After 返回 time.After(d)。
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
