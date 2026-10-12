package extension

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

func TestParseSkillFrontmatter(t *testing.T) {
	content := "\uFEFF\n---\n# 注释\nname: polite-greeting\ndescription: >-\n  Use a concise, polite greeting.\n  Trigger when the user says hello.\n\n  Second paragraph.\nversion: \"1.2.3\"\nallowed-tools: read_file run_command\nlicense: 'MIT'\nmetadata:\n  custom:\n    emoji: \"👋\"\ncolor: blue # 行尾注释\n---\n\n# Polite Greeting\n\nBody text.\n"
	m, body, err := ParseSkill(content)
	if err != nil {
		t.Fatalf("解析应成功: %v", err)
	}
	if m.Name != "polite-greeting" || m.Version != "1.2.3" {
		t.Errorf("标量字段不符: %+v", m)
	}
	wantDesc := "Use a concise, polite greeting. Trigger when the user says hello.\nSecond paragraph."
	if m.Description != wantDesc {
		t.Errorf("折叠块标量应按空格拼接、空行保留换行:\n得到 %q\n期望 %q", m.Description, wantDesc)
	}
	if strings.Join(m.AllowedTools, ",") != "read_file,run_command" {
		t.Errorf("allowed-tools 应按空格拆分: %v", m.AllowedTools)
	}
	if m.Extra["license"] != "MIT" || m.Extra["color"] != "blue" {
		t.Errorf("其它标量应进入 Extra: %v", m.Extra)
	}
	if _, ok := m.Extra["metadata"]; ok && m.Extra["metadata"] != "" {
		t.Errorf("嵌套映射应被跳过: %v", m.Extra)
	}
	if body != "# Polite Greeting\n\nBody text.\n" {
		t.Errorf("正文不符: %q", body)
	}
}

func TestParseSkillLiteralBlockAndLists(t *testing.T) {
	content := "---\nname: a1\ndescription: |\n  line one\n    indented\n  line three\nallowed-tools: [\"read_file\", 'write_file', run_command]\n---\nbody"
	m, body, err := ParseSkill(content)
	if err != nil {
		t.Fatalf("解析应成功: %v", err)
	}
	if m.Description != "line one\n  indented\nline three" {
		t.Errorf("字面块标量应保留换行与相对缩进: %q", m.Description)
	}
	if strings.Join(m.AllowedTools, ",") != "read_file,write_file,run_command" {
		t.Errorf("行内列表不符: %v", m.AllowedTools)
	}
	if body != "body" {
		t.Errorf("正文不符: %q", body)
	}
	if _, _, err := ParseSkill("---\r\nname: crlf\r\ndescription: \"windows line endings\"\r\n---\r\nbody\r\n"); err != nil {
		t.Errorf("CRLF 应被接受: %v", err)
	}
}

func TestParseSkillErrors(t *testing.T) {
	cases := map[string]string{
		"没有 frontmatter":   "# title\nname: x\n",
		"没有结束符":            "---\nname: x\ndescription: y\n",
		"缺少 name":          "---\ndescription: y\n---\n",
		"缺少 description":   "---\nname: x\n---\n",
		"name 大写":          "---\nname: BadName\ndescription: y\n---\n",
		"name 下划线":         "---\nname: bad_name\ndescription: y\n---\n",
		"name 连字符开头":       "---\nname: -bad\ndescription: y\n---\n",
		"name 过长":          "---\nname: " + strings.Repeat("a", 65) + "\ndescription: y\n---\n",
		"非 key value 行":    "---\nname: x\njust text\ndescription: y\n---\n",
		"重复键":              "---\nname: x\nname: y\ndescription: y\n---\n",
		"allowed-tools 非法": "---\nname: x\ndescription: y\nallowed-tools: read_file ../x\n---\n",
	}
	for name, content := range cases {
		if _, _, err := ParseSkill(content); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s: 应返回 CodeInvalid，得到 %v", name, err)
		}
	}
}

func TestParsePluginManifest(t *testing.T) {
	raw := `{
	  "name": " Weather ",
	  "version": "1.0.0",
	  "kind": "plugin",
	  "entry": "python3",
	  "args": ["main.py"],
	  "permissions": {"network": true},
	  "future_field": {"ignored": true},
	  "tools": [
	    {"name": "forecast", "description": "Get forecast", "input_schema": {"type":"object","properties":{"city":{"type":"string"}}}},
	    {"name": "ping", "description": "No args"}
	  ]
	}`
	m, err := ParsePluginManifest([]byte(raw))
	if err != nil {
		t.Fatalf("解析应成功: %v", err)
	}
	if m.Name != "Weather" || m.Kind != "plugin" || m.Entry != "python3" || len(m.Args) != 1 || !m.Permissions.Network {
		t.Errorf("字段不符: %+v", m)
	}
	if string(m.Tools[1].InputSchema) != `{"type":"object"}` {
		t.Errorf("缺省 schema 应补为对象: %s", m.Tools[1].InputSchema)
	}
	if !json.Valid(m.Tools[0].InputSchema) || !strings.Contains(string(m.Tools[0].InputSchema), "city") {
		t.Errorf("schema 应原样保留: %s", m.Tools[0].InputSchema)
	}
	if _, err := ParsePluginManifest([]byte(`{"name":"x","entry":"e","tools":[{"name":"t"}]}`)); err != nil {
		t.Errorf("kind 可省略: %v", err)
	}
	bad := []string{
		`not json`,
		`{"name":"","entry":"e","tools":[{"name":"t"}]}`,
		`{"name":"x","entry":"","tools":[{"name":"t"}]}`,
		`{"name":"x","entry":"e","tools":[]}`,
		`{"name":"x","entry":"e","kind":"mcp_server","tools":[{"name":"t"}]}`,
		`{"name":"x","entry":"e","tools":[{"name":"bad name"}]}`,
		`{"name":"x","entry":"e","tools":[{"name":"t"},{"name":"t"}]}`,
		`{"name":"x","entry":"e","tools":[{"name":"t","input_schema":"string"}]}`,
		`{"name":"x","version":"` + strings.Repeat("9", 40) + `","entry":"e","tools":[{"name":"t"}]}`,
	}
	for _, raw := range bad {
		if _, err := ParsePluginManifest([]byte(raw)); !model.IsCode(err, model.CodeInvalid) {
			t.Errorf("%s: 应返回 CodeInvalid，得到 %v", raw, err)
		}
	}
}

func TestSanitizeAndUniqueToolNames(t *testing.T) {
	cases := map[string]string{
		"search":                "search",
		"fetch page":            "fetch_page",
		"天气查询":                  "tool",
		"get/weather!!":         "get_weather",
		"__x__":                 "__x__",
		"a--b":                  "a--b",
		"":                      "tool",
		strings.Repeat("a", 70): strings.Repeat("a", 64),
	}
	for in, want := range cases {
		if got := sanitizeToolName(in); got != want {
			t.Errorf("sanitizeToolName(%q) = %q，期望 %q", in, got, want)
		}
	}
	used := map[string]struct{}{}
	if got := uniqueToolName("search", used); got != "search" {
		t.Errorf("首个应保留原名，得到 %q", got)
	}
	if got := uniqueToolName("search", used); got != "search_2" {
		t.Errorf("第二个应加 _2，得到 %q", got)
	}
	if got := uniqueToolName("search", used); got != "search_3" {
		t.Errorf("第三个应加 _3，得到 %q", got)
	}
	long := strings.Repeat("b", 64)
	used[long] = struct{}{}
	if got := uniqueToolName(long, used); len(got) != 64 || !strings.HasSuffix(got, "_2") {
		t.Errorf("加后缀时应保持在 64 字符内，得到 %q", got)
	}
}
