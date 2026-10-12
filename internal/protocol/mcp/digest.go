package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// DigestPrefix 是 ToolsDigest 返回值的算法前缀。
const DigestPrefix = "sha256:"

// digestEntry 是参与摘要的字段：名称、描述、参数 schema。Source 与 Risk 由本地决定，不参与。
type digestEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolsDigest 计算一组工具声明的稳定摘要："sha256:<hex>"。
//
// 工具按名称排序，schema 先解析再重新序列化（对象键按字典序），因此键顺序与空白差异不影响结果；
// 名称、描述或 schema 任一变化都会改变摘要。空列表也有确定的摘要。
func ToolsDigest(specs []model.ToolSpec) string {
	entries := make([]digestEntry, 0, len(specs))
	for _, s := range specs {
		entries = append(entries, digestEntry{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: canonicalJSON(s.InputSchema),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Description < entries[j].Description
	})
	// entries 的每个字段都能序列化，这里不会失败。
	raw, _ := json.Marshal(entries)
	sum := sha256.Sum256(raw)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// canonicalJSON 把任意 JSON 重新序列化为键有序、无多余空白的形式；无法解析时原样返回。
func canonicalJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}
