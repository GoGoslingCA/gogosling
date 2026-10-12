package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestNewValidatesInputs(t *testing.T) {
	repo := newMemRepo()
	runner := newFakeRunner()
	if _, err := New(nil, runner, nil, nil, Options{}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少仓储应返回 invalid，得到 %v", err)
	}
	if _, err := New(repo, nil, nil, nil, Options{}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("缺少 Runner 应返回 invalid，得到 %v", err)
	}
	if _, err := New(repo, runner, nil, nil, Options{DefaultTimezone: "Mars/Olympus"}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非法默认时区应返回 invalid，得到 %v", err)
	}
	svc, err := New(repo, runner, nil, nil, Options{})
	if err != nil {
		t.Fatalf("默认参数应能创建服务: %v", err)
	}
	if svc.DefaultLocation() != time.Local || svc.tick != DefaultTick || svc.log == nil {
		t.Error("默认时区应为系统时区、节拍应为 DefaultTick、日志不应为 nil")
	}
	svc, err = New(repo, runner, nil, slog.New(slog.DiscardHandler), Options{DefaultTimezone: "Asia/Shanghai", Tick: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if svc.DefaultLocation().String() != "Asia/Shanghai" || svc.tick != time.Minute {
		t.Error("显式配置应生效")
	}
}

func TestCreateFillsDefaultsAndNextRun(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(s *JobSpec) {
		s.Name = ""
		s.Input = "\n\n  早上好，  今天的  日程  \n第二行"
		s.MissedPolicy = ""
		s.Schedule = "  0 9 * * *  "
		s.Timezone = " Asia/Shanghai "
	})
	if !model.IsID(job.ID) {
		t.Errorf("应生成 ULID，得到 %q", job.ID)
	}
	if job.Name != "早上好， 今天的 日程" {
		t.Errorf("名称应取输入的第一个非空行并压缩空白，得到 %q", job.Name)
	}
	if job.Input != "早上好，  今天的  日程  \n第二行" {
		t.Errorf("输入应只去除首尾空白，得到 %q", job.Input)
	}
	if job.MissedPolicy != model.MissedSkip {
		t.Errorf("错过策略应默认 skip，得到 %q", job.MissedPolicy)
	}
	if job.Schedule != "0 9 * * *" || job.Timezone != "Asia/Shanghai" {
		t.Errorf("表达式与时区应去除空白，得到 %q / %q", job.Schedule, job.Timezone)
	}
	if !job.CreatedAt.Equal(baseTime) || !job.UpdatedAt.Equal(baseTime) {
		t.Errorf("时间戳应来自注入的时钟，得到 %s / %s", job.CreatedAt, job.UpdatedAt)
	}
	mustEqualTime(t, "NextRunAt", job.NextRunAt, time.Date(2026, 1, 13, 1, 0, 0, 0, time.UTC))
	if job.LastStatus != "" || job.LastRunAt != nil {
		t.Error("新任务不应有执行记录")
	}
	stored := env.repo.get(t, job.ID)
	if stored.Name != job.Name || stored.NextRunAt == nil || !stored.NextRunAt.Equal(*job.NextRunAt) {
		t.Error("返回值应与持久化内容一致")
	}
	// 返回的是副本：修改它不影响内存状态。
	job.Name = "改掉"
	if env.repo.get(t, job.ID).Name == "改掉" || env.svc.entries[job.ID].job.Name == "改掉" {
		t.Error("Create 应返回副本")
	}
}

func TestCreateDisabledHasNoNextRun(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(s *JobSpec) { s.Enabled = false })
	if job.NextRunAt != nil || job.Enabled {
		t.Errorf("停用的任务不应有 NextRunAt，得到 %+v", job)
	}
}

func TestCreateNameLongerThanLimitIsRejectedButDerivedNameIsTruncated(t *testing.T) {
	env := newTestEnv(t, Options{})
	long := strings.Repeat("字", MaxNameRunes+1)
	if _, err := env.svc.Create(context.Background(), JobSpec{
		AgentID: "a", Name: long, Schedule: "@daily", Kind: model.JobPushText, Input: "x", Enabled: true,
	}); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("超长名称应返回 invalid，得到 %v", err)
	}
	job := env.create(t, func(s *JobSpec) { s.Name = ""; s.Input = strings.Repeat("长", 100) })
	if got := []rune(job.Name); len(got) != nameFromInputRunes+1 || got[len(got)-1] != '…' {
		t.Errorf("从输入推导的名称应截断到 %d 个字符并加省略号，得到 %q", nameFromInputRunes, job.Name)
	}
}

func TestCreateValidation(t *testing.T) {
	env := newTestEnv(t, Options{})
	valid := JobSpec{AgentID: "a", Name: "n", Schedule: "@daily", Kind: model.JobRunAgent, Input: "hi", MissedPolicy: model.MissedRunOnce, Enabled: true}
	cases := map[string]func(*JobSpec){
		"缺少 agent_id": func(s *JobSpec) { s.AgentID = "  " },
		"kind 为空":     func(s *JobSpec) { s.Kind = "" },
		"未知 kind":     func(s *JobSpec) { s.Kind = "email" },
		"input 为空":    func(s *JobSpec) { s.Input = " \n " },
		"input 超长":    func(s *JobSpec) { s.Input = strings.Repeat("a", MaxInputRunes+1) },
		"表达式非法":       func(s *JobSpec) { s.Schedule = "every day" },
		"带 TZ 前缀":     func(s *JobSpec) { s.Schedule = "TZ=UTC @daily" },
		"时区非法":        func(s *JobSpec) { s.Timezone = "Nowhere/City" },
		"错过策略非法":      func(s *JobSpec) { s.MissedPolicy = "retry" },
		"@every 间隔过短": func(s *JobSpec) { s.Schedule = "@every 10s" },
	}
	for name, mutate := range cases {
		spec := valid
		mutate(&spec)
		if _, err := env.svc.Create(context.Background(), spec); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s 应返回 invalid，得到 %v", name, err)
		}
	}
	if jobs, _ := env.repo.ListJobs(context.Background(), ""); len(jobs) != 0 {
		t.Errorf("校验失败不应写入仓储，得到 %d 条", len(jobs))
	}
	if _, err := env.svc.Create(context.Background(), valid); err != nil {
		t.Errorf("合法输入应成功: %v", err)
	}
}

func TestUpdateRecomputesNextRunOnlyWhenScheduleChanges(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(s *JobSpec) { s.Schedule = "@every 1h" })
	first := *job.NextRunAt
	env.clock.Advance(10 * time.Minute)

	renamed, err := env.svc.Update(context.Background(), job.ID, JobPatch{Name: ptr("新名字"), Input: ptr("新内容")})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "新名字" || renamed.Input != "新内容" {
		t.Errorf("字段应被更新，得到 %+v", renamed)
	}
	mustEqualTime(t, "只改名称时 NextRunAt", renamed.NextRunAt, first)
	if !renamed.UpdatedAt.Equal(baseTime.Add(10 * time.Minute)) {
		t.Errorf("UpdatedAt 应更新，得到 %s", renamed.UpdatedAt)
	}

	changed, err := env.svc.Update(context.Background(), job.ID, JobPatch{Schedule: ptr("0 9 * * *"), Timezone: ptr("America/New_York")})
	if err != nil {
		t.Fatal(err)
	}
	mustEqualTime(t, "改表达式后 NextRunAt", changed.NextRunAt, time.Date(2026, 1, 12, 14, 0, 0, 0, time.UTC))
	if env.svc.entries[job.ID].sched.Location().String() != "America/New_York" {
		t.Error("内存中的解析结果应随更新替换")
	}
	stored := env.repo.get(t, job.ID)
	if stored.Schedule != "0 9 * * *" || stored.Timezone != "America/New_York" || stored.Name != "新名字" {
		t.Errorf("持久化内容应更新，得到 %+v", stored)
	}
}

func TestUpdateInvalidPatchLeavesJobUntouched(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, nil)
	before := env.repo.get(t, job.ID)
	for name, patch := range map[string]JobPatch{
		"非法表达式": {Schedule: ptr("nope")},
		"非法时区":  {Timezone: ptr("Nowhere/City")},
		"非法类型":  {Kind: ptr(model.JobKind("x"))},
		"空输入":   {Input: ptr("")},
	} {
		if _, err := env.svc.Update(context.Background(), job.ID, patch); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s 应返回 invalid，得到 %v", name, err)
		}
	}
	after := env.repo.get(t, job.ID)
	if after.Schedule != before.Schedule || after.Timezone != before.Timezone || after.Kind != before.Kind ||
		after.Input != before.Input || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("校验失败不应改动任务: %+v → %+v", before, after)
	}
	if _, err := env.svc.Update(context.Background(), "missing", JobPatch{Name: ptr("x")}); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("不存在的任务应返回 not_found，得到 %v", err)
	}
}

func TestEnableDisable(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, nil)
	disabled, err := env.svc.Disable(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.NextRunAt != nil {
		t.Errorf("停用后应清空 NextRunAt，得到 %+v", disabled)
	}
	env.clock.Set(time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC))
	enabled, err := env.svc.Enable(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled {
		t.Error("应被启用")
	}
	mustEqualTime(t, "重新启用后 NextRunAt 应从现在算起", enabled.NextRunAt, time.Date(2026, 1, 16, 9, 0, 0, 0, time.UTC))
	// 停用的任务在节拍中不会被触发。
	env.svc.Disable(context.Background(), job.ID)
	env.clock.Set(time.Date(2026, 1, 16, 9, 0, 5, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)
}

func TestGetListDelete(t *testing.T) {
	env := newTestEnv(t, Options{})
	a := env.create(t, func(s *JobSpec) { s.AgentID = "agent-a" })
	env.clock.Advance(time.Second)
	b := env.create(t, func(s *JobSpec) { s.AgentID = "agent-b" })

	got, err := env.svc.Get(context.Background(), a.ID)
	if err != nil || got.ID != a.ID {
		t.Fatalf("Get 应返回任务: %v", err)
	}
	all, _ := env.svc.List(context.Background(), "")
	if len(all) != 2 || all[0].ID != a.ID || all[1].ID != b.ID {
		t.Errorf("List 应按创建时间返回全部任务，得到 %d 条", len(all))
	}
	onlyB, _ := env.svc.List(context.Background(), "agent-b")
	if len(onlyB) != 1 || onlyB[0].ID != b.ID {
		t.Errorf("List 应按智能体过滤，得到 %d 条", len(onlyB))
	}
	if err := env.svc.Delete(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.Get(context.Background(), a.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("删除后应为 not_found，得到 %v", err)
	}
	if _, ok := env.svc.entries[a.ID]; ok {
		t.Error("删除后应从内存移除")
	}
	if err := env.svc.Delete(context.Background(), a.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("重复删除应返回 not_found，得到 %v", err)
	}
}

func TestTriggerNowRunsImmediatelyAndPublishesEvent(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, func(s *JobSpec) { s.Enabled = false })
	sub := env.bus.Subscribe(context.Background(), 0, 8, nil)
	defer sub.Cancel()

	env.clock.Advance(time.Minute)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Fatalf("手动触发失败: %v", err)
	}
	ran := env.runner.next(t)
	if ran.ID != job.ID || ran.Input != "早上好" {
		t.Errorf("Runner 应收到任务副本，得到 %+v", ran)
	}
	select {
	case ev := <-sub.C:
		if ev.Type != EventJobTriggered || ev.AgentID != "agent-1" {
			t.Errorf("事件类型或智能体不对: %+v", ev)
		}
		payload, ok := ev.Payload.(model.Job)
		if !ok || payload.ID != job.ID {
			t.Errorf("事件 payload 应为 Job 值，得到 %T", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("应发布 job.triggered 事件")
	}
	env.waitIdle(t, job.ID)
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunOK || stored.LastError != "" {
		t.Errorf("执行成功应记录 ok，得到 %q / %q", stored.LastStatus, stored.LastError)
	}
	mustEqualTime(t, "LastRunAt", stored.LastRunAt, baseTime.Add(time.Minute))
	if stored.NextRunAt != nil || stored.Enabled {
		t.Error("手动触发不应改变启用状态与 NextRunAt")
	}
}

func TestTriggerNowConflictsWhileRunning(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.runner.block = make(chan struct{})
	job := env.create(t, nil)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	env.runner.next(t)
	if err := env.svc.TriggerNow(context.Background(), job.ID); !model.IsCode(err, model.CodeConflict) {
		t.Errorf("执行中再次触发应返回 conflict，得到 %v", err)
	}
	if err := env.svc.TriggerNow(context.Background(), "missing"); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("不存在的任务应返回 not_found，得到 %v", err)
	}
	close(env.runner.block)
	env.waitIdle(t, job.ID)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Errorf("执行结束后应可再次触发: %v", err)
	}
	env.runner.next(t)
	env.waitIdle(t, job.ID)
}

func TestRunErrorIsRecordedWithoutUnderlyingCause(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.runner.err = &model.Error{Code: model.CodeDenied, Message: "被权限规则拒绝", Err: errors.New("secret=abc")}
	job := env.create(t, nil)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	env.runner.next(t)
	env.waitIdle(t, job.ID)
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunFailed {
		t.Errorf("失败应记录 failed，得到 %q", stored.LastStatus)
	}
	if stored.LastError != "denied: 被权限规则拒绝" {
		t.Errorf("LastError 应只含错误码与消息，得到 %q", stored.LastError)
	}

	env.runner.err = errors.New("plain failure")
	env.svc.TriggerNow(context.Background(), job.ID)
	env.runner.next(t)
	env.waitIdle(t, job.ID)
	if got := env.repo.get(t, job.ID).LastError; got != "plain failure" {
		t.Errorf("普通错误应记录其文本，得到 %q", got)
	}
}

func TestRunPanicIsConvertedToFailure(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.runner.panicWith = "boom"
	job := env.create(t, nil)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	env.runner.next(t)
	env.waitIdle(t, job.ID)
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunFailed || !strings.Contains(stored.LastError, "panic") {
		t.Errorf("panic 应被记录为失败，得到 %q / %q", stored.LastStatus, stored.LastError)
	}
}

func TestRunTimeoutCancelsRunner(t *testing.T) {
	env := newTestEnv(t, Options{RunTimeout: 20 * time.Millisecond})
	env.runner.block = make(chan struct{})
	job := env.create(t, nil)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	env.runner.next(t)
	env.waitIdle(t, job.ID)
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunFailed || !strings.Contains(stored.LastError, "deadline") {
		t.Errorf("超时应记录失败，得到 %q / %q", stored.LastStatus, stored.LastError)
	}
}

func TestDeleteDuringRunDropsResult(t *testing.T) {
	env := newTestEnv(t, Options{})
	env.runner.block = make(chan struct{})
	job := env.create(t, nil)
	if err := env.svc.TriggerNow(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	env.runner.next(t)
	if err := env.svc.Delete(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	close(env.runner.block)
	env.waitIdle(t, job.ID)
	if _, err := env.repo.GetJob(context.Background(), job.ID); !model.IsCode(err, model.CodeNotFound) {
		t.Errorf("删除的任务不应被写回，得到 %v", err)
	}
}

func TestPersistFailureCanRetrySchedulingAfterRecovery(t *testing.T) {
	env := newTestEnv(t, Options{})
	job := env.create(t, nil)
	env.repo.updateErr = errors.New("disk full")
	env.clock.Set(time.Date(2026, 1, 12, 9, 0, 5, 0, time.UTC))
	env.svc.tickOnce(context.Background())
	env.runner.expectNoCall(t)
	// 内存保留原到期时间，写库恢复后仍能重试本次触发。
	mustEqualTime(t, "内存中的 NextRunAt", env.svc.entries[job.ID].job.NextRunAt, *job.NextRunAt)
	env.repo.updateErr = nil
	env.svc.tickOnce(context.Background())
	env.runner.next(t)
	env.waitIdle(t, job.ID)
	stored := env.repo.get(t, job.ID)
	if stored.LastStatus != model.JobRunOK || stored.NextRunAt == nil {
		t.Errorf("恢复后的触发应记录完整状态，得到 %+v", stored)
	}
}
