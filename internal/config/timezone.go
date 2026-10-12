package config

import (
	"os"
	"strings"
	"time"
)

// 系统时区的探测来源；测试替换为临时文件。
var (
	localtimePath = "/etc/localtime"
	timezonePath  = "/etc/timezone"
)

// SystemTimezone 尽力返回本机时区的 IANA 名称（例如 "Asia/Shanghai"）；无法确定时返回空串。
//
// 顺序：TZ 环境变量（去掉前导 ":"；也接受指向 zoneinfo 目录内文件的路径）
// > /etc/localtime 符号链接目标中 "zoneinfo/" 之后的部分（Linux、macOS）
// > /etc/timezone 的内容（Debian 系）。候选值必须能被 time.LoadLocation 解析且不是 "Local"。
// Windows 上通常返回空串：此时内核使用 time.Local，客户端应按自己的本地时区展示。
// getenv 为 nil 时使用 os.Getenv。
func SystemTimezone(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if tz := strings.TrimPrefix(strings.TrimSpace(getenv("TZ")), ":"); tz != "" {
		if name, ok := ianaName(zoneSuffix(tz)); ok {
			return name
		}
	}
	if target, err := os.Readlink(localtimePath); err == nil {
		if name, ok := ianaName(zoneSuffix(target)); ok {
			return name
		}
	}
	if raw, err := os.ReadFile(timezonePath); err == nil {
		if name, ok := ianaName(strings.TrimSpace(string(raw))); ok {
			return name
		}
	}
	return ""
}

// zoneSuffix 取路径中最后一个 "zoneinfo/" 之后的部分；不含该片段时原样返回。
func zoneSuffix(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "zoneinfo/"); i >= 0 {
		return p[i+len("zoneinfo/"):]
	}
	return p
}

// ianaName 校验候选名称是可加载的 IANA 时区。
func ianaName(name string) (string, bool) {
	name = strings.Trim(strings.TrimSpace(name), "/")
	if name == "" || name == "Local" || strings.HasPrefix(name, ".") {
		return "", false
	}
	if _, err := time.LoadLocation(name); err != nil {
		return "", false
	}
	return name, true
}
