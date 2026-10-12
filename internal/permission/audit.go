package permission

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// 本文件负责"写出去之前先脱敏"：审批请求的 Details 与审计记录的 Details
// 都只能包含路径、命令、目标域名这类描述性信息，不能包含文件内容、命令输出或凭据。

const (
	// maxDetailString 是脱敏后单个字符串的最大长度（按字符计）。
	maxDetailString = 512
	// maxCommandInAudit 是审计记录中命令字符串的最大长度。
	maxCommandInAudit = 200
	// maxDetailKeys 是脱敏后对象保留的最大键数。
	maxDetailKeys = 32
	// maxDetailItems 是脱敏后数组保留的最大元素数。
	maxDetailItems = 16
	// maxDetailDepth 是脱敏时递归的最大深度。
	maxDetailDepth = 3
	// maxAuditPaths 是审计记录中保留的路径条数。
	maxAuditPaths = 8
)

// sensitiveKeyParts 命中即视为凭据，整个键丢弃。
var sensitiveKeyParts = []string{
	"token", "secret", "password", "passwd", "credential", "api_key", "apikey",
	"authorization", "cookie", "private_key", "session_key", "access_key",
}

// contentKeys 是可能携带文件内容或命令输出的键，整个键丢弃。
var contentKeys = map[string]bool{
	"content": true, "contents": true, "data": true, "body": true, "text": true,
	"stdin": true, "input": true, "output": true, "stdout": true, "stderr": true,
	"diff": true, "patch": true, "env": true, "environment": true, "headers": true,
	"payload": true, "file_content": true, "new_content": true, "old_content": true,
	"old_string": true, "new_string": true, "edits": true,
}

// isDroppedKey 判断键是否应当从脱敏结果中移除。
func isDroppedKey(k string) bool {
	low := strings.ToLower(k)
	if contentKeys[low] {
		return true
	}
	for _, part := range sensitiveKeyParts {
		if strings.Contains(low, part) {
			return true
		}
	}
	return false
}

// truncateString 按字符截断并附加标记。
func truncateString(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…(truncated)"
}

// sanitizeValue 递归脱敏任意 JSON 值；返回 nil 表示整个值被丢弃。
func sanitizeValue(v any, depth int, redacted *[]string) any {
	switch t := v.(type) {
	case string:
		return truncateString(t, maxDetailString)
	case map[string]any:
		if depth >= maxDetailDepth {
			return nil
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(keys))
		for _, k := range keys {
			if isDroppedKey(k) {
				*redacted = append(*redacted, k)
				continue
			}
			if len(out) >= maxDetailKeys {
				break
			}
			if sv := sanitizeValue(t[k], depth+1, redacted); sv != nil {
				out[k] = sv
			}
		}
		return out
	case []any:
		if depth >= maxDetailDepth {
			return nil
		}
		n := len(t)
		if n > maxDetailItems {
			n = maxDetailItems
		}
		out := make([]any, 0, n)
		for _, it := range t[:n] {
			if sv := sanitizeValue(it, depth+1, redacted); sv != nil {
				out = append(out, sv)
			}
		}
		return out
	default:
		// 数字、布尔、null 原样保留。
		return v
	}
}

// sanitizeDetails 把调用方给的 Details 脱敏成可展示、可持久化的对象。
// 非对象或无法解析的输入返回空 map，调用方据此决定是否写入。
func sanitizeDetails(raw json.RawMessage) map[string]any {
	m := parseObject(raw)
	if m == nil {
		return map[string]any{}
	}
	var redacted []string
	out, _ := sanitizeValue(m, 0, &redacted).(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	if len(redacted) > 0 {
		sort.Strings(redacted)
		out["redacted_keys"] = redacted
	}
	return out
}

// limitPaths 截断路径列表的长度与每条路径的长度。
func limitPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	n := len(paths)
	if n > maxAuditPaths {
		n = maxAuditPaths
	}
	out := make([]string, 0, n)
	for _, p := range paths[:n] {
		out = append(out, truncateString(p, 256))
	}
	return out
}

// marshalDetails 把 map 编码为 JSON；失败时返回 nil（审计记录允许没有 Details）。
func marshalDetails(m map[string]any) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return raw
}
