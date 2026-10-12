package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
)

//go:embed locales/*.json
var localeFS embed.FS

const (
	// LocaleZH 是简体中文。
	LocaleZH = "zh"
	// LocaleEN 是英文，也是兜底语言。
	LocaleEN = "en"
	// LocaleFR 是法语。
	LocaleFR = "fr"
	// DefaultLocale 是找不到任何语言信号时使用的语言。
	DefaultLocale = LocaleEN
)

// supported 是受支持的语言，顺序即 Locales 的返回顺序。
var supported = []string{LocaleZH, LocaleEN, LocaleFR}

// Locales 返回受支持的语言代码副本。
func Locales() []string { return append([]string(nil), supported...) }

// ErrorCodes 镜像 model.Code 的全部取值；语言包必须为每个码提供 errors.<code>。
// 本包是叶子包不能导入 model，因此新增错误码时需要同步更新这里与各语言包（测试会校验语言包）。
var ErrorCodes = []string{
	"invalid", "unauthorized", "forbidden", "not_found", "conflict",
	"denied", "unavailable", "timeout", "cancelled", "internal",
}

// BuiltinTools 是内置工具名（含 Implicit 工具 load_skill）；语言包必须为每个工具提供 tools.<name>。
var BuiltinTools = []string{"read_file", "write_file", "edit_file", "delete_file", "list_dir", "search_files", "run_command", "fetch_url", "load_skill", "read_skill_resource"}

// runErrorPrefix 是运行错误文案的键前缀。
const runErrorPrefix = "run.error."

// RunErrorKey 返回运行错误机器常量（写入 Run.Error 的 "daemon restarted"、"worker crashed" 等）
// 对应的 i18n 键：run.error.<常量中的空格换成下划线>。常量只能由小写字母、数字与单个空格组成，
// 否则返回 false——运行时给出的自由文本（如 "worker exited: exit status 1"）不是常量，应原样展示。
func RunErrorKey(code string) (string, bool) {
	if code == "" || strings.TrimSpace(code) != code || strings.Contains(code, "  ") {
		return "", false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == ' ') {
			return "", false
		}
	}
	return runErrorPrefix + strings.ReplaceAll(code, " ", "_"), true
}

// RunErrorText 返回运行错误机器常量的本地化文案；不是已登记的常量时返回 false，调用方原样展示。
func RunErrorText(locale, code string) (string, bool) {
	key, ok := RunErrorKey(code)
	if !ok {
		return "", false
	}
	return Lookup(locale, key)
}

// RunErrorTexts 返回 locale 下全部已登记的 运行错误机器常量 → 本地化文案（由 run.error.* 键反推常量）。
// 组合根用它填充网关等需要整张映射表的模块；每次调用返回新的 map。
func RunErrorTexts(locale string) map[string]string {
	out := map[string]string{}
	keys, err := Keys(LocaleEN)
	if err != nil {
		return out
	}
	for _, k := range keys {
		name, ok := strings.CutPrefix(k, runErrorPrefix)
		if !ok || strings.Contains(name, ".") {
			continue
		}
		out[strings.ReplaceAll(name, "_", " ")] = T(locale, k, nil)
	}
	return out
}

// tables 是 语言 → 扁平键 → 文案。
type tables map[string]map[string]string

// load 只解析一次嵌入的语言包；失败时每次调用都返回同一个错误。
var load = sync.OnceValues(func() (tables, error) {
	out := make(tables, len(supported))
	for _, loc := range supported {
		raw, err := localeFS.ReadFile("locales/" + loc + ".json")
		if err != nil {
			return nil, fmt.Errorf("i18n: 读取语言包 %s 失败: %w", loc, err)
		}
		var tree map[string]any
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, fmt.Errorf("i18n: 语言包 %s 不是合法 JSON: %w", loc, err)
		}
		flat := map[string]string{}
		if err := flatten("", tree, flat); err != nil {
			return nil, fmt.Errorf("i18n: 语言包 %s: %w", loc, err)
		}
		out[loc] = flat
	}
	return out, nil
})

// flatten 把嵌套对象展开为点分键；叶子必须是非空字符串，键名不得含点。
func flatten(prefix string, node map[string]any, out map[string]string) error {
	for k, v := range node {
		if k == "" || strings.Contains(k, ".") {
			return fmt.Errorf("键 %q 不合法（不能为空或包含点）", prefix+k)
		}
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch val := v.(type) {
		case string:
			if val == "" {
				return fmt.Errorf("键 %q 的文案为空", path)
			}
			out[path] = val
		case map[string]any:
			if err := flatten(path, val, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("键 %q 的值必须是字符串或对象", path)
		}
	}
	return nil
}

// Check 校验嵌入的语言包可以解析；组合根应在启动时调用，把缺陷尽早暴露出来。
func Check() error {
	_, err := load()
	return err
}

// Lookup 返回 locale 下 key 对应的原始文案（未插值）。
// locale 先规范化（zh-CN → zh，不支持的值 → en）；该语言没有时回退到 en；仍没有返回 false。
func Lookup(locale, key string) (string, bool) {
	t, err := load()
	if err != nil {
		return "", false
	}
	loc, ok := Normalize(locale)
	if !ok {
		loc = DefaultLocale
	}
	if s, ok := t[loc][key]; ok {
		return s, true
	}
	if loc != DefaultLocale {
		if s, ok := t[DefaultLocale][key]; ok {
			return s, true
		}
	}
	return "", false
}

// T 返回 locale 下 key 的文案并用 args 插值；找不到 key 时返回 key 本身，保证界面总有可读内容。
func T(locale, key string, args map[string]any) string {
	s, ok := Lookup(locale, key)
	if !ok {
		s = key
	}
	return Interpolate(s, args)
}

// Interpolate 把 s 中的 {name} 替换为 args[name]（用 fmt.Sprint 格式化）。
// 没有对应参数的占位符原样保留；花括号内不是标识符的片段也原样保留。
func Interpolate(s string, args map[string]any) string {
	if len(args) == 0 || !strings.Contains(s, "{") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		open := strings.IndexByte(s[i:], '{')
		if open < 0 {
			b.WriteString(s[i:])
			break
		}
		open += i
		b.WriteString(s[i:open])
		closeIdx := strings.IndexByte(s[open:], '}')
		if closeIdx < 0 {
			b.WriteString(s[open:])
			break
		}
		closeIdx += open
		name := s[open+1 : closeIdx]
		if v, ok := args[name]; ok && isIdent(name) {
			b.WriteString(fmt.Sprint(v))
		} else {
			b.WriteString(s[open : closeIdx+1])
		}
		i = closeIdx + 1
	}
	return b.String()
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}

// Keys 返回 locale 语言包的全部扁平键（升序），供测试与工具使用。
func Keys(locale string) ([]string, error) {
	t, err := load()
	if err != nil {
		return nil, err
	}
	loc, ok := Normalize(locale)
	if !ok {
		return nil, fmt.Errorf("i18n: 不支持的语言 %q", locale)
	}
	keys := make([]string, 0, len(t[loc]))
	for k := range t[loc] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// ErrorMessage 返回错误码对应的本地化文案（errors.<code>）；未知错误码按 internal 处理。
func ErrorMessage(locale, code string, args map[string]any) string {
	if _, ok := Lookup(locale, "errors."+code); !ok {
		code = "internal"
	}
	return T(locale, "errors."+code, args)
}

// ToolName 返回工具的显示名：内置工具取 tools.<name>；扩展提供的工具没有文案时
// 把下划线与连字符替换为空格并首字母大写（mcp_search_docs → Mcp search docs）；空名返回"未知工具"。
func ToolName(locale, name string) string {
	if name == "" {
		return T(locale, "tools.unknown", nil)
	}
	if s, ok := Lookup(locale, "tools."+name); ok {
		return s
	}
	return humanize(name)
}

func humanize(name string) string {
	cleaned := strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(name))
	if cleaned == "" {
		return name
	}
	runes := []rune(cleaned)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
