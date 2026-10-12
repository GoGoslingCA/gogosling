package i18n

import (
	"sort"
	"strconv"
	"strings"
)

// channelLocales 是 IM 通道类型到默认语言的提示：国内平台的用户绝大多数说中文。
// 键是 model.ChannelKind 的字符串值（本包不能导入 model），未列出的通道不提供提示。
var channelLocales = map[string]string{
	"feishu":   LocaleZH,
	"dingtalk": LocaleZH,
	"wecom":    LocaleZH,
	"weixin":   LocaleZH,
	"wechat":   LocaleZH,
	"qq":       LocaleZH,
	"telegram": LocaleEN,
	"discord":  LocaleEN,
	"slack":    LocaleEN,
	"whatsapp": LocaleEN,
}

// Normalize 把语言标签规范化为受支持的语言代码：忽略大小写与地区（zh-CN、zh_TW、en-US、fr-FR），
// 只看主子标签。不受支持的标签返回 false，调用方应继续尝试下一个来源而不是误判为默认语言。
func Normalize(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", false
	}
	s = strings.ReplaceAll(s, "_", "-")
	// 语言标签只含字母、数字与连字符；带有 ";q=" 之类头部语法的值不是标签，交给 FromAcceptLanguage。
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return "", false
		}
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	switch s {
	case LocaleZH:
		return LocaleZH, true
	case LocaleEN:
		return LocaleEN, true
	case LocaleFR:
		return LocaleFR, true
	}
	return "", false
}

// envLocaleKeys 是按 POSIX 优先级查看的语言环境变量。
var envLocaleKeys = []string{"LC_ALL", "LC_MESSAGES", "LANG"}

// FromEnv 按 LC_ALL > LC_MESSAGES > LANG 的顺序从进程环境推断语言：
// "zh_CN.UTF-8"、"en_US@euro"、"fr_FR.UTF-8" 这类 POSIX 写法先去掉编码与修饰符再规范化；
// 不受支持的值（C、POSIX、ja_JP…）被跳过而不是误判。getenv 为 nil 时返回 false。
func FromEnv(getenv func(string) string) (string, bool) {
	if getenv == nil {
		return "", false
	}
	for _, key := range envLocaleKeys {
		v := strings.TrimSpace(getenv(key))
		if i := strings.IndexAny(v, ".@"); i >= 0 {
			v = v[:i]
		}
		if loc, ok := Normalize(v); ok {
			return loc, true
		}
	}
	return "", false
}

// EffectiveLocale 是内核"实际生效语言"的唯一解析：设置项 > LC_ALL / LC_MESSAGES / LANG > DefaultLocale。
//
// setting 是用户保存的语言设置（未设置时为空）；守护进程、CLI 与 GET /settings 的 effective_locale
// 都用它，保证"界面显示的语言"与"内核实际使用的语言"一致。
func EffectiveLocale(setting string, getenv func(string) string) string {
	if loc, ok := Normalize(setting); ok {
		return loc
	}
	if loc, ok := FromEnv(getenv); ok {
		return loc
	}
	return DefaultLocale
}

// ResolveLocale 判定应使用的语言：用户偏好 > Accept-Language（按 q 值排序）> 通道提示 > en。
// 每个来源只在给出受支持的语言时才生效；例如偏好为 ja 时会继续看后面的来源。
func ResolveLocale(userPref, acceptLanguage, channelHint string) string {
	if loc, ok := Normalize(userPref); ok {
		return loc
	}
	if loc, ok := FromAcceptLanguage(acceptLanguage); ok {
		return loc
	}
	if loc, ok := channelLocales[strings.ToLower(strings.TrimSpace(channelHint))]; ok {
		return loc
	}
	return DefaultLocale
}

// FromAcceptLanguage 解析 HTTP Accept-Language 头（RFC 9110 §12.5.4）：
// 按 q 值从高到低（同值保持原顺序）取第一个受支持的语言；q=0 与通配符 * 被跳过。
// 无法解析的 q 值按 1 处理，宁可多匹配也不丢弃客户端的意图。
func FromAcceptLanguage(header string) (string, bool) {
	if strings.TrimSpace(header) == "" {
		return "", false
	}
	type candidate struct {
		tag string
		q   float64
	}
	var cands []candidate
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		tag := strings.TrimSpace(fields[0])
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, param := range fields[1:] {
			param = strings.TrimSpace(param)
			if len(param) > 2 && (param[0] == 'q' || param[0] == 'Q') && param[1] == '=' {
				if v, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64); err == nil {
					q = v
				}
			}
		}
		if q <= 0 {
			continue
		}
		cands = append(cands, candidate{tag: tag, q: q})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].q > cands[j].q })
	for _, c := range cands {
		if loc, ok := Normalize(c.tag); ok {
			return loc, true
		}
	}
	return "", false
}
