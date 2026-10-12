package scheduler

import (
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// 本文件负责把 Job.Schedule + Job.Timezone 解析为可计算下一次触发时间的 Schedule。
// 只使用 robfig/cron 的 Parser 与 Schedule.Next；调度循环由本包自己实现（见 loop.go）。

// MinInterval 是 @every 描述符允许的最小间隔。更短的间隔小于调度节拍，无法按期触发，
// 也容易把任务变成热循环，因此直接拒绝。
const MinInterval = time.Minute

// everyPrefix 是 robfig/cron 的固定间隔描述符前缀。
const everyPrefix = "@every "

// parser 只接受标准五字段表达式与描述符；它是无状态的纯解析器，可被并发使用。
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Schedule 是解析后的触发规则：表达式按指定时区解释，Next 的结果统一为 UTC。
type Schedule struct {
	spec  string
	sched cron.Schedule
	loc   *time.Location
	// interval 标记 @every 描述符：它的触发链相对上一次触发而不是对齐墙上时钟，
	// 唤醒重算时需要区别对待（见 Service.resync）。
	interval bool
}

// LoadLocation 校验 IANA 时区名并返回对应的 Location。
//
// 空名字返回 nil 表示"使用默认时区"；"Local" 与无法加载的名字返回 CodeInvalid——
// 持久化的任务不应依赖运行它的机器的本地时区设置。
func LoadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	if name == "Local" {
		return nil, model.Errorf(model.CodeInvalid, "时区必须是 IANA 名称（例如 Asia/Shanghai），不接受 %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, &model.Error{Code: model.CodeInvalid, Message: "未知的时区 " + name, Err: err}
	}
	return loc, nil
}

// ParseSchedule 解析并校验表达式与时区；timezone 为空时使用 defaultLoc（为 nil 时取系统时区）。
//
// 接受：标准五字段（分 时 日 月 周）、@hourly / @daily / @midnight / @weekly / @monthly /
// @yearly / @annually、@every <duration>（不小于 MinInterval）。
// 拒绝：空表达式、TZ= / CRON_TZ= 前缀（时区请用 Job.Timezone，且该前缀在 robfig/cron 中
// 遇到没有空格的输入会越界）、永远不会触发的表达式（例如 2 月 30 日）。
func ParseSchedule(spec, timezone string, defaultLoc *time.Location) (*Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, model.NewError(model.CodeInvalid, "cron 表达式不能为空")
	}
	if strings.HasPrefix(spec, "TZ=") || strings.HasPrefix(spec, "CRON_TZ=") {
		return nil, model.Errorf(model.CodeInvalid, "cron 表达式不接受 TZ= 前缀，请使用 timezone 字段: %q", spec)
	}

	loc, err := LoadLocation(timezone)
	if err != nil {
		return nil, err
	}
	if loc == nil {
		loc = defaultLoc
	}
	if loc == nil {
		loc = time.Local
	}

	sched, err := parser.Parse(spec)
	if err != nil {
		return nil, &model.Error{
			Code:    model.CodeInvalid,
			Message: "cron 表达式 " + spec + " 无法解析: " + err.Error(),
			Details: map[string]any{"schedule": spec},
			Err:     err,
		}
	}
	s := &Schedule{spec: spec, sched: sched, loc: loc, interval: strings.HasPrefix(spec, everyPrefix)}
	if s.interval {
		if cd, ok := sched.(cron.ConstantDelaySchedule); ok && cd.Delay < MinInterval {
			return nil, model.Errorf(model.CodeInvalid, "@every 的间隔不能小于 %s: %q", MinInterval, spec)
		}
	}
	if s.Next(time.Now()).IsZero() {
		return nil, model.Errorf(model.CodeInvalid, "cron 表达式永远不会触发: %q", spec)
	}
	return s, nil
}

// Spec 返回规范化（去除首尾空白）后的表达式。
func (s *Schedule) Spec() string { return s.spec }

// Location 返回表达式被解释的时区。
func (s *Schedule) Location() *time.Location { return s.loc }

// Interval 报告表达式是否为 @every 固定间隔。
func (s *Schedule) Interval() bool { return s.interval }

// Next 返回严格晚于 after 的下一次触发时间（UTC）；表达式在可预见范围内没有下一次时返回零值。
//
// robfig/cron 的 SpecSchedule 在没有 TZ= 前缀时按传入时间的时区计算，因此先把 after 转到
// 任务时区再计算，结果再转回 UTC。
func (s *Schedule) Next(after time.Time) time.Time {
	next := s.sched.Next(after.In(s.loc))
	if next.IsZero() {
		return time.Time{}
	}
	return next.UTC()
}

// maxMissedScan 限制统计错过次数时向前推演的步数，避免长时间离线的高频任务让一次节拍变慢。
const maxMissedScan = 1000

// missedBetween 统计 (due, now] 区间内还有多少个本应触发的时间点（不含 due 本身），最多数到 maxMissedScan。
//
// 结果为 0 表示 due 只是迟到了不到一个周期；大于 0 表示错过超过一个周期，应适用 MissedPolicy。
func (s *Schedule) missedBetween(due, now time.Time) int {
	count := 0
	t := due
	for count < maxMissedScan {
		t = s.Next(t)
		if t.IsZero() || t.After(now) {
			break
		}
		count++
	}
	return count
}
