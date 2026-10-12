package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// decodeArgs 把调用参数解析到 dst；空参数视为 {}。非对象或类型不匹配返回 CodeInvalid。
func decodeArgs(raw json.RawMessage, dst any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	if raw[0] != '{' {
		return model.NewError(model.CodeInvalid, "参数必须是 JSON 对象")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return model.Wrap(model.CodeInvalid, "参数解析失败: "+describeJSONError(err), err)
	}
	return nil
}

// describeJSONError 把 encoding/json 的错误转成不含 Go 内部类型名的说明。
func describeJSONError(err error) string {
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		if ute.Field != "" {
			return "字段 " + ute.Field + " 的类型应为 " + ute.Type.String() + "，实际为 " + ute.Value
		}
		return "类型应为 " + ute.Type.String() + "，实际为 " + ute.Value
	}
	return err.Error()
}

// objectSchema 生成一个 JSON Schema 对象声明；props 的键为参数名。
// 用代码生成而不是手写字符串，保证每个工具的 InputSchema 都是合法 JSON。
func objectSchema(props map[string]any, required ...string) json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	} else {
		schema["required"] = []string{}
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		// props 只包含字面量 map 与基本类型，Marshal 不会失败。
		panic("tools: 生成 JSON Schema 失败: " + err.Error())
	}
	return raw
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func intProp(desc string, min, max int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": min, "maximum": max}
}

// cleanRelPath 对模型给出的相对路径做最轻量的规范化：去首尾空白，空值视为根目录 "."。
// 真正的越界检查由 Workspace 完成。
func cleanRelPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "."
	}
	return p
}

// firstLine 返回文本的首行，超过 max 个字符时截断并加省略号；用于摘要。
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + " …"
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
