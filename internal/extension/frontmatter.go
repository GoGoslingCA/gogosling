package extension

import (
	"bufio"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

// SkillFile 是技能目录里的清单文件名（Agent Skills 规范）。
const SkillFile = "SKILL.md"

// MaxSkillNameLength 是技能名称的最大长度。
const MaxSkillNameLength = 64

// skillNameRe 遵循 Agent Skills 规范：小写字母、数字与连字符，不以连字符开头或结尾。
var skillNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// SkillManifest 是 SKILL.md 的 frontmatter。
type SkillManifest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version,omitempty"`
	// AllowedTools 是技能声明需要的工具名（可选）；只作提示，不扩展智能体的权限。
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Extra 保留其余的顶层标量字段（例如 license、metadata 以外的自定义键），不解释其含义。
	Extra map[string]string `json:"extra,omitempty"`
}

// ParseSkill 解析 SKILL.md 的内容，返回 frontmatter 与正文。
//
// 这是一个刻意轻量的解析器（不引入 YAML 依赖），只支持：
//   - 文件开头的 "---" 块（允许前导空行），到下一个 "---" 行结束；
//   - 顶层 "key: value" 标量；值可以用单引号或双引号包裹；
//   - 折叠 / 字面块标量（"key: >-" / "key: |" 等）：收集随后缩进的行；
//   - allowed-tools：空格分隔的字符串，或 "[a, b]" 形式的行内列表；
//   - "#" 开头的注释行与空行。
//
// 不支持：嵌套映射（如 metadata:）会被整体跳过；多行列表（"- item"）不解析；锚点、别名、多文档。
// 这些限制覆盖了 Agent Skills 规范要求的全部必填与可选字段。
func ParseSkill(content string) (SkillManifest, string, error) {
	block, body, ok := splitFrontmatter(content)
	if !ok {
		return SkillManifest{}, "", model.NewError(model.CodeInvalid, "SKILL.md 缺少 YAML frontmatter（以 --- 包围的块）")
	}
	fields, err := parseScalars(block)
	if err != nil {
		return SkillManifest{}, "", err
	}
	m := SkillManifest{Extra: map[string]string{}}
	for k, v := range fields {
		switch k {
		case "name":
			m.Name = v
		case "description":
			m.Description = v
		case "version":
			m.Version = v
		case "allowed-tools", "allowed_tools":
			m.AllowedTools = parseList(v)
		default:
			m.Extra[k] = v
		}
	}
	if len(m.Extra) == 0 {
		m.Extra = nil
	}
	if err := validateSkillManifest(m); err != nil {
		return SkillManifest{}, "", err
	}
	return m, body, nil
}

func validateSkillManifest(m SkillManifest) error {
	if m.Name == "" {
		return model.NewError(model.CodeInvalid, "SKILL.md 缺少必填字段 name")
	}
	if len(m.Name) > MaxSkillNameLength || !skillNameRe.MatchString(m.Name) {
		return model.Errorf(model.CodeInvalid, "技能名 %q 不合法：只能是小写字母、数字与连字符，且不超过 %d 个字符", m.Name, MaxSkillNameLength)
	}
	if strings.TrimSpace(m.Description) == "" {
		return model.NewError(model.CodeInvalid, "SKILL.md 缺少必填字段 description")
	}
	if len(m.Description) > 1024 {
		return model.NewError(model.CodeInvalid, "description 不能超过 1024 个字符")
	}
	for _, tool := range m.AllowedTools {
		if !toolNameRe.MatchString(tool) {
			return model.Errorf(model.CodeInvalid, "allowed-tools 中的工具名 %q 不合法", tool)
		}
	}
	return nil
}

// splitFrontmatter 把文件切成 frontmatter 块与正文；没有合法块时 ok 为 false。
func splitFrontmatter(content string) (block, body string, ok bool) {
	content = strings.TrimPrefix(content, "\uFEFF")
	lines := strings.SplitAfter(content, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || strings.TrimRight(lines[i], "\r\n") != "---" {
		return "", "", false
	}
	start := i + 1
	for j := start; j < len(lines); j++ {
		if strings.TrimRight(lines[j], "\r\n") == "---" {
			block = strings.Join(lines[start:j], "")
			body = strings.TrimLeft(strings.Join(lines[j+1:], ""), "\r\n")
			return block, body, true
		}
	}
	return "", "", false
}

// parseScalars 解析顶层 "key: value" 行；返回键到值的映射。
func parseScalars(block string) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(block))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var (
		pending   string // 正在收集块标量的键
		pendingOp string // 块标量指示符
		pendingLn []string
	)
	flush := func() {
		if pending == "" {
			return
		}
		out[pending] = joinBlock(pendingOp, pendingLn)
		pending, pendingOp, pendingLn = "", "", nil
	}
	for sc.Scan() {
		raw := strings.TrimRight(sc.Text(), "\r")
		if pending != "" {
			if raw == "" || raw[0] == ' ' || raw[0] == '\t' {
				pendingLn = append(pendingLn, raw)
				continue
			}
			flush()
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if raw[0] == ' ' || raw[0] == '\t' {
			// 缩进行属于上一个（未被支持的）嵌套结构，跳过。
			continue
		}
		key, value, found := strings.Cut(raw, ":")
		if !found {
			return nil, model.Errorf(model.CodeInvalid, "frontmatter 行 %q 不是 key: value 形式", trimmed)
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t\"'") {
			return nil, model.Errorf(model.CodeInvalid, "frontmatter 键 %q 不合法", key)
		}
		if _, dup := out[key]; dup {
			return nil, model.Errorf(model.CodeInvalid, "frontmatter 键 %q 重复", key)
		}
		value = strings.TrimSpace(value)
		switch value {
		case ">", ">-", ">+", "|", "|-", "|+":
			pending, pendingOp = key, value
			out[key] = ""
			continue
		case "":
			// 嵌套映射或空值：记录为空串，后续缩进行被跳过。
			out[key] = ""
			continue
		}
		out[key] = unquote(stripComment(value))
	}
	if err := sc.Err(); err != nil {
		return nil, model.Wrap(model.CodeInvalid, "frontmatter 读取失败", err)
	}
	flush()
	return out, nil
}

// joinBlock 把块标量的行按指示符拼接：">" 折叠为空格，"|" 保留换行；"-" 去掉末尾换行。
func joinBlock(op string, lines []string) string {
	// 去掉公共缩进。
	indent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	var parts []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, l[indent:])
	}
	// 去掉尾部空行。
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if strings.HasPrefix(op, ">") {
		var b strings.Builder
		for i, p := range parts {
			if p == "" {
				b.WriteString("\n")
				continue
			}
			if i > 0 && parts[i-1] != "" {
				b.WriteString(" ")
			}
			b.WriteString(p)
		}
		return strings.TrimSpace(b.String())
	}
	return strings.Join(parts, "\n")
}

// stripComment 去掉未加引号的值末尾的 " # 注释"。
func stripComment(v string) string {
	if strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'") {
		return v
	}
	if i := strings.Index(v, " #"); i >= 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}

// unquote 去掉成对的引号；双引号内处理常见转义。
func unquote(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '"' && v[len(v)-1] == '"':
			var s string
			if err := json.Unmarshal([]byte(v), &s); err == nil {
				return s
			}
			return v[1 : len(v)-1]
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
	}
	return v
}

// parseList 解析 "[a, b]" 或空格 / 逗号分隔的列表。
func parseList(v string) []string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	var out []string
	for _, item := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		item = unquote(strings.TrimSpace(item))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
