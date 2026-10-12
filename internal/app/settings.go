package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/config"
	"github.com/GoGoslingCA/gogosling/internal/i18n"
	"github.com/GoGoslingCA/gogosling/internal/model"
	"github.com/GoGoslingCA/gogosling/internal/scheduler"
	"github.com/GoGoslingCA/gogosling/internal/server"
	"github.com/GoGoslingCA/gogosling/internal/session"
)

// settings 是启动时读取、交给引擎、网关、调度器、提示构造器与本地 API 的全局设置。
// 这些模块在构造时取值，运行期修改设置需要重启守护进程才对它们生效（server 自己热更新设置项本身）。
type settings struct {
	// Timezone 是设置项中已校验的 IANA 时区名；为空表示未设置（调度器使用系统时区）。
	Timezone string
	// EffectiveTimezone 是内核实际使用的时区：Timezone，未设置时为系统时区的 IANA 名称
	// （config.SystemTimezone；无法确定时为空，表示 time.Local）。
	EffectiveTimezone string
	// Locale 是内核实际使用的语言（i18n.EffectiveLocale：设置项 > 系统环境 > 默认），zh、en 或 fr。
	Locale      string
	MaxParallel int
}

// loadSettings 读取全局设置并解析实际生效值；单项无效时忽略该项并记录警告，不让启动失败。
// 设置项的键与 JSON 编码沿用 server 包（server.Setting*），本包不另定义一份。
func loadSettings(ctx context.Context, repo model.SettingsRepo, getenv func(string) string, logger *slog.Logger) (settings, error) {
	var s settings
	s.MaxParallel = session.DefaultParallelRuns
	if saved, err := settingString(ctx, repo, server.SettingMaxParallel); err != nil {
		return s, err
	} else if saved != "" {
		if n, e := strconv.Atoi(saved); e == nil && n >= 1 && n <= 8 {
			s.MaxParallel = n
		} else {
			logger.Warn("忽略无效的并行任务设置")
		}
	}

	tz, err := settingString(ctx, repo, server.SettingTimezone)
	if err != nil {
		return s, err
	}
	if tz != "" {
		if _, lerr := scheduler.LoadLocation(tz); lerr != nil {
			logger.Warn("设置中的时区无效，改用系统时区", "timezone", tz, "error", lerr)
		} else {
			s.Timezone = tz
		}
	}
	s.EffectiveTimezone = s.Timezone
	if s.EffectiveTimezone == "" {
		s.EffectiveTimezone = config.SystemTimezone(getenv)
	}

	loc, err := settingString(ctx, repo, server.SettingLocale)
	if err != nil {
		return s, err
	}
	if _, ok := i18n.Normalize(loc); loc != "" && !ok {
		logger.Warn("设置中的语言不受支持，改用系统环境语言", "locale", loc)
	}
	s.Locale = i18n.EffectiveLocale(loc, getenv)
	return s, nil
}

// settingString 读取字符串设置：值是 JSON 字符串时解码，否则按原文（与 server 的读取规则一致）；
// 不存在时返回空串。
func settingString(ctx context.Context, repo model.SettingsRepo, key string) (string, error) {
	raw, err := repo.GetSetting(ctx, key)
	if err != nil {
		if model.IsCode(err, model.CodeNotFound) {
			return "", nil
		}
		return "", err
	}
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		return strings.TrimSpace(s), nil
	}
	return strings.TrimSpace(raw), nil
}
