package i18n

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	if err := Check(); err != nil {
		t.Fatalf("嵌入的语言包应能解析: %v", err)
	}
}

func TestLocalesShareSameKeyTree(t *testing.T) {
	zh, err := Keys(LocaleZH)
	if err != nil {
		t.Fatal(err)
	}
	if len(zh) == 0 {
		t.Fatal("语言包不应为空")
	}
	for _, loc := range Locales() {
		keys, err := Keys(loc)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(zh, keys) {
			t.Errorf("%s 与 zh 的键树必须一致\nzh=%v\n%s=%v", loc, zh, loc, keys)
		}
	}
}

func TestErrorCodesCovered(t *testing.T) {
	for _, loc := range Locales() {
		for _, code := range ErrorCodes {
			if _, ok := Lookup(loc, "errors."+code); !ok {
				t.Errorf("%s 缺少 errors.%s", loc, code)
			}
		}
	}
	// 语言包里不应出现 ErrorCodes 之外的错误码，避免两边漂移。
	known := map[string]bool{}
	for _, c := range ErrorCodes {
		known[c] = true
	}
	keys, _ := Keys(LocaleEN)
	for _, k := range keys {
		if strings.HasPrefix(k, "errors.") && !known[strings.TrimPrefix(k, "errors.")] {
			t.Errorf("语言包包含未登记的错误码 %s", k)
		}
	}
}

func TestBuiltinToolsCovered(t *testing.T) {
	for _, loc := range Locales() {
		for _, name := range BuiltinTools {
			if _, ok := Lookup(loc, "tools."+name); !ok {
				t.Errorf("%s 缺少 tools.%s", loc, name)
			}
		}
		if _, ok := Lookup(loc, "tools.unknown"); !ok {
			t.Errorf("%s 缺少 tools.unknown", loc)
		}
	}
}

func TestTInterpolationAndFallback(t *testing.T) {
	got := T("zh", "approval.requested", map[string]any{"tool": "run_command"})
	if got != "智能体想要执行 run_command，需要你的批准。" {
		t.Errorf("中文插值不符: %q", got)
	}
	got = T("en-US", "approval.approved", map[string]any{"tool": "read_file"})
	if got != "Approved: read_file" {
		t.Errorf("en-US 应规范化为 en: %q", got)
	}
	got = T("fr", "common.yes", nil)
	if got != "Oui" {
		t.Errorf("fr 应使用法文: %q", got)
	}
	got = T("fr-FR", "common.yes", nil)
	if got != "Oui" {
		t.Errorf("fr-FR 应规范化为 fr: %q", got)
	}
	got = T("ja", "common.yes", nil)
	if got != "Yes" {
		t.Errorf("不支持的语言应回退到 en: %q", got)
	}
	got = T("zh_CN", "common.yes", nil)
	if got != "是" {
		t.Errorf("zh_CN 应规范化为 zh: %q", got)
	}
	got = T("zh", "no.such.key", map[string]any{"x": 1})
	if got != "no.such.key" {
		t.Errorf("缺失的键应返回键本身而不是 panic: %q", got)
	}
	got = T("en", "approval.requested", nil)
	if got != "The assistant wants to use {tool} and needs your approval." {
		t.Errorf("缺少参数时占位符应原样保留: %q", got)
	}
}

func TestInterpolate(t *testing.T) {
	cases := []struct {
		in   string
		args map[string]any
		want string
	}{
		{"{a} and {b}", map[string]any{"a": 1, "b": "x"}, "1 and x"},
		{"{a} {a}", map[string]any{"a": "r"}, "r r"},
		{"{missing} {a}", map[string]any{"a": 2}, "{missing} 2"},
		{"literal { brace", map[string]any{"a": 2}, "literal { brace"},
		{"{not an ident}", map[string]any{"not an ident": 1}, "{not an ident}"},
		{"no placeholders", map[string]any{"a": 2}, "no placeholders"},
		{"{a}", nil, "{a}"},
		{"{a} 后缀", map[string]any{"a": 3.5}, "3.5 后缀"},
	}
	for _, c := range cases {
		if got := Interpolate(c.in, c.args); got != c.want {
			t.Errorf("Interpolate(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestErrorMessageAndToolName(t *testing.T) {
	if got := ErrorMessage("zh", "not_found", nil); got != "请求的资源不存在。" {
		t.Errorf("errors.not_found 中文不符: %q", got)
	}
	if got := ErrorMessage("en", "bogus_code", nil); got != T("en", "errors.internal", nil) {
		t.Errorf("未知错误码应按 internal 处理: %q", got)
	}
	if got := ToolName("zh", "run_command"); got != "执行命令" {
		t.Errorf("内置工具显示名不符: %q", got)
	}
	if got := ToolName("en", "mcp_search-docs"); got != "Mcp search docs" {
		t.Errorf("扩展工具应被人性化: %q", got)
	}
	if got := ToolName("en", ""); got != "Unknown tool" {
		t.Errorf("空工具名应显示未知工具: %q", got)
	}
}

func TestLocalePlaceholdersMatch(t *testing.T) {
	zhKeys, err := Keys(LocaleZH)
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range Locales() {
		for _, key := range zhKeys {
			base, _ := Lookup(LocaleZH, key)
			other, ok := Lookup(loc, key)
			if !ok {
				t.Errorf("%s 缺少 %s", loc, key)
				continue
			}
			if !reflect.DeepEqual(bracePlaceholders(base), bracePlaceholders(other)) {
				t.Errorf("%s 的 %s 占位符不一致\nzh=%q\n%s=%q", loc, key, base, loc, other)
			}
		}
	}
}

func bracePlaceholders(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		j := i + 1
		for j < len(s) && (s[j] == '_' || (s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= '0' && s[j] <= '9')) {
			j++
		}
		if j > i+1 && j < len(s) && s[j] == '}' {
			out = append(out, s[i:j+1])
			i = j
		}
	}
	sort.Strings(out)
	return out
}

func TestKeysRejectsUnsupportedLocale(t *testing.T) {
	if _, err := Keys("ja"); err == nil {
		t.Error("不支持的语言应返回错误")
	}
	if _, err := Keys("fr"); err != nil {
		t.Errorf("fr 应是受支持的语言: %v", err)
	}
}

func TestFlattenValidation(t *testing.T) {
	cases := []struct {
		name string
		tree map[string]any
	}{
		{"数值叶子", map[string]any{"a": map[string]any{"b": 1}}},
		{"空字符串", map[string]any{"a": ""}},
		{"键含点", map[string]any{"a.b": "x"}},
		{"空键", map[string]any{"": "x"}},
		{"数组", map[string]any{"a": []any{"x"}}},
	}
	for _, c := range cases {
		if err := flatten("", c.tree, map[string]string{}); err == nil {
			t.Errorf("%s 应被拒绝", c.name)
		}
	}
	out := map[string]string{}
	if err := flatten("", map[string]any{"a": map[string]any{"b": "x"}, "c": "y"}, out); err != nil {
		t.Fatal(err)
	}
	if out["a.b"] != "x" || out["c"] != "y" || len(out) != 2 {
		t.Errorf("展开结果不符: %v", out)
	}
}

func TestRunErrorKeyAndText(t *testing.T) {
	// session.Error* 与 engine.Error* 的全部取值（本包不能导入它们；internal/app 的测试再按常量本身核对一次）。
	for code, want := range map[string]string{
		"daemon restarted":    "run.error.daemon_restarted",
		"run timeout":         "run.error.run_timeout",
		"persist failed":      "run.error.persist_failed",
		"run failed":          "run.error.run_failed",
		"worker unavailable":  "run.error.worker_unavailable",
		"worker crashed":      "run.error.worker_crashed",
		"worker stopped":      "run.error.worker_stopped",
		"run rejected":        "run.error.run_rejected",
		"run cancelled":       "run.error.run_cancelled",
		"invalid run request": "run.error.invalid_run_request",
	} {
		key, ok := RunErrorKey(code)
		if !ok || key != want {
			t.Errorf("RunErrorKey(%q) = (%q, %v)，期望 %q", code, key, ok, want)
		}
		for _, loc := range Locales() {
			if _, ok := RunErrorText(loc, code); !ok {
				t.Errorf("%s 缺少运行错误常量 %q 的文案", loc, code)
			}
		}
	}
	for _, free := range []string{"", " run failed", "worker exited: exit status 1", "Run Failed", "run  failed", "运行失败"} {
		if _, ok := RunErrorKey(free); ok {
			t.Errorf("自由文本 %q 不应被当作运行错误常量", free)
		}
	}
	if _, ok := RunErrorText("zh", "no such constant"); ok {
		t.Error("未登记的常量应返回 false")
	}
}

func TestRunErrorTextsCoversAllRunErrorKeys(t *testing.T) {
	zh := RunErrorTexts("zh")
	if len(zh) == 0 {
		t.Fatal("运行错误映射不应为空")
	}
	for _, loc := range Locales() {
		if texts := RunErrorTexts(loc); len(texts) != len(zh) {
			t.Fatalf("%s 的运行错误映射应与 zh 条目数一致：%d / %d", loc, len(texts), len(zh))
		}
	}
	en, fr := RunErrorTexts("en"), RunErrorTexts("fr")
	if zh["daemon restarted"] != "守护进程重启，运行已中断" {
		t.Errorf("zh 映射不正确：%q", zh["daemon restarted"])
	}
	if en["worker crashed"] == "" || en["worker crashed"] == zh["worker crashed"] {
		t.Errorf("en 映射应是英文文案：%q", en["worker crashed"])
	}
	if fr["worker crashed"] == "" || fr["worker crashed"] == zh["worker crashed"] || fr["worker crashed"] == en["worker crashed"] {
		t.Errorf("fr 映射应是法文文案：%q", fr["worker crashed"])
	}
	for code, text := range zh {
		if got, ok := RunErrorText("zh", code); !ok || got != text {
			t.Errorf("RunErrorTexts 与 RunErrorText 对 %q 不一致：%q / %q", code, text, got)
		}
	}
	zh["daemon restarted"] = "changed"
	if RunErrorTexts("zh")["daemon restarted"] == "changed" {
		t.Error("RunErrorTexts 每次应返回新的 map")
	}
}
