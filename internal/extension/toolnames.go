package extension

import (
	"strconv"
	"strings"
)

// sanitizeToolName 把任意名称改写为模型接口接受的形式：非法字符替换为下划线，去掉首尾下划线，
// 截断到上限；结果为空时返回 "tool"。原名由调用方在路由表里保留，用于分发。
func sanitizeToolName(name string) string {
	if toolNameRe.MatchString(name) {
		return name
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		ok := r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if ok {
			b.WriteRune(r)
			lastUnderscore = r == '_'
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if len(out) > MaxToolNameLength {
		out = strings.TrimRight(out[:MaxToolNameLength], "_")
	}
	if out == "" {
		return "tool"
	}
	return out
}

// uniqueToolName 在 used 中为 base 找一个未被占用的名字：先用 base 本身，再依次尝试 base_2、base_3……
// 返回的名字会被加入 used。
func uniqueToolName(base string, used map[string]struct{}) string {
	if _, taken := used[base]; !taken {
		used[base] = struct{}{}
		return base
	}
	for i := 2; ; i++ {
		suffix := "_" + strconv.Itoa(i)
		stem := base
		if len(stem)+len(suffix) > MaxToolNameLength {
			stem = strings.TrimRight(stem[:MaxToolNameLength-len(suffix)], "_")
		}
		candidate := stem + suffix
		if _, taken := used[candidate]; !taken {
			used[candidate] = struct{}{}
			return candidate
		}
	}
}
