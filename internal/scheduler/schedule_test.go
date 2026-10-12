package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestParseScheduleAcceptsFiveFieldsAndDescriptors(t *testing.T) {
	for _, spec := range []string{
		"0 9 * * *", "*/5 * * * *", "30 8 * * 1-5", "0 0 1 * *", "  0 9 * * *  ",
		"@hourly", "@daily", "@midnight", "@weekly", "@monthly", "@yearly", "@annually",
		"@every 1m", "@every 1h30m", "@every 24h",
	} {
		s, err := ParseSchedule(spec, "", time.UTC)
		if err != nil {
			t.Errorf("%q 应被接受，得到 %v", spec, err)
			continue
		}
		if s.Spec() != strings.TrimSpace(spec) {
			t.Errorf("%q 的规范化结果应去除首尾空白，得到 %q", spec, s.Spec())
		}
		if next := s.Next(baseTime); next.IsZero() || !next.After(baseTime) {
			t.Errorf("%q 的下一次触发应晚于起点，得到 %s", spec, next)
		}
	}
}

func TestParseScheduleRejectsInvalid(t *testing.T) {
	for _, spec := range []string{
		"", "   ", "bad", "0 9 * *", "0 9 * * * *", "60 * * * *", "0 25 * * *",
		"TZ=Asia/Shanghai 0 9 * * *", "CRON_TZ=UTC @daily", "TZ=Asia/Shanghai",
		"@reboot", "@every", "@every abc", "@every 30s", "@every -1h", "@every 0s",
		"0 0 30 2 *",
	} {
		if _, err := ParseSchedule(spec, "", time.UTC); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%q 应返回 invalid 错误，得到 %v", spec, err)
		}
	}
}

func TestParseScheduleMarksInterval(t *testing.T) {
	every, _ := ParseSchedule("@every 1h", "", time.UTC)
	daily, _ := ParseSchedule("@daily", "", time.UTC)
	if !every.Interval() {
		t.Error("@every 应标记为固定间隔")
	}
	if daily.Interval() {
		t.Error("@daily 不应标记为固定间隔")
	}
	// @every 的触发链相对于起点，不对齐墙上时钟。
	if next := every.Next(baseTime); !next.Equal(baseTime.Add(time.Hour)) {
		t.Errorf("@every 1h 的下一次应为起点 + 1h，得到 %s", next)
	}
}

func TestScheduleNextHonorsTimezone(t *testing.T) {
	cases := []struct {
		tz   string
		want time.Time
	}{
		{"UTC", time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)},
		{"Asia/Shanghai", time.Date(2026, 1, 13, 1, 0, 0, 0, time.UTC)},     // 当天 16:59 CST 已过 9 点，次日 09:00 CST
		{"America/New_York", time.Date(2026, 1, 12, 14, 0, 0, 0, time.UTC)}, // 03:59 EST，当天 09:00 EST
	}
	for _, c := range cases {
		s, err := ParseSchedule("0 9 * * *", c.tz, time.UTC)
		if err != nil {
			t.Fatalf("时区 %s 应合法: %v", c.tz, err)
		}
		got := s.Next(baseTime)
		if !got.Equal(c.want) {
			t.Errorf("时区 %s 的下一次应为 %s，得到 %s", c.tz, c.want, got)
		}
		if got.Location() != time.UTC {
			t.Errorf("Next 的结果应为 UTC，得到 %s", got.Location())
		}
	}
}

func TestScheduleNextUsesDefaultLocationWhenTimezoneEmpty(t *testing.T) {
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	s, err := ParseSchedule("0 9 * * *", "", shanghai)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 13, 1, 0, 0, 0, time.UTC)
	if got := s.Next(baseTime); !got.Equal(want) {
		t.Errorf("时区为空时应使用默认时区，期望 %s，得到 %s", want, got)
	}
	if s.Location() != shanghai {
		t.Error("Location 应返回默认时区")
	}
}

func TestLoadLocation(t *testing.T) {
	if loc, err := LoadLocation(""); err != nil || loc != nil {
		t.Errorf("空时区应返回 nil, nil，得到 %v, %v", loc, err)
	}
	if loc, err := LoadLocation(" Asia/Tokyo "); err != nil || loc == nil || loc.String() != "Asia/Tokyo" {
		t.Errorf("应去除空白并加载 Asia/Tokyo，得到 %v, %v", loc, err)
	}
	for _, bad := range []string{"Local", "Mars/Olympus", "GMT+8:00"} {
		if _, err := LoadLocation(bad); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%q 应返回 invalid 错误，得到 %v", bad, err)
		}
	}
	if _, err := ParseSchedule("@daily", "Mars/Olympus", time.UTC); !model.IsCode(err, model.CodeInvalid) {
		t.Errorf("非法时区应让 ParseSchedule 失败，得到 %v", err)
	}
}

func TestMissedBetween(t *testing.T) {
	daily, _ := ParseSchedule("0 9 * * *", "UTC", time.UTC)
	due := time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		now  time.Time
		want int
	}{
		{due, 0},
		{due.Add(5 * time.Second), 0},
		{due.Add(23 * time.Hour), 0},                // 迟到不到一个周期
		{due.Add(24 * time.Hour), 1},                // 恰好到了下一次
		{due.Add(24*time.Hour + 30*time.Minute), 1}, // 错过一次
		{due.Add(72 * time.Hour), 3},
	}
	for _, c := range cases {
		if got := daily.missedBetween(due, c.now); got != c.want {
			t.Errorf("now=%s 时错过次数应为 %d，得到 %d", c.now, c.want, got)
		}
	}
	every, _ := ParseSchedule("@every 1m", "UTC", time.UTC)
	if got := every.missedBetween(due, due.Add(48*time.Hour)); got != maxMissedScan {
		t.Errorf("长时间离线的高频任务应在 %d 处封顶，得到 %d", maxMissedScan, got)
	}
}
